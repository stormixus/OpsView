//go:build onnx

package lpr

import (
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var (
	ortOnce sync.Once
	ortErr  error
)

func initORT(libPath string) error {
	ortOnce.Do(func() {
		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}
		ortErr = ort.InitializeEnvironment()
	})
	return ortErr
}

// Engine runs plate detector + OCR ONNX models in-process.
type Engine struct {
	cfg         Config
	plateCfg    PlateConfig
	detSess     *ort.AdvancedSession
	ocrSess     *ort.AdvancedSession
	detIn       *ort.Tensor[float32]
	detOut      *ort.Tensor[float32]
	ocrIn       *ort.Tensor[uint8]
	ocrOut      *ort.Tensor[float32]
	detSize     int
	detInName   string
	detOutName  string
	ocrInName   string
	ocrOutName  string
}

func newEngine(cfg Config) (Recognizer, error) {
	if cfg.DetectorONNX == "" || cfg.OCRONNX == "" || cfg.OCRConfigYAML == "" {
		return nil, fmt.Errorf("RELAY_LPR=1 requires RELAY_LPR_DETECTOR, RELAY_LPR_OCR, RELAY_LPR_OCR_CONFIG")
	}
	for _, p := range []string{cfg.DetectorONNX, cfg.OCRONNX, cfg.OCRConfigYAML} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("model path %q: %w", p, err)
		}
	}
	if err := initORT(cfg.ORTLib); err != nil {
		return nil, fmt.Errorf("onnxruntime init: %w", err)
	}
	plateCfg, err := LoadPlateConfig(cfg.OCRConfigYAML)
	if err != nil {
		return nil, err
	}
	eng := &Engine{cfg: cfg, plateCfg: plateCfg}
	if err := eng.initDetector(); err != nil {
		return nil, err
	}
	if err := eng.initOCR(); err != nil {
		eng.close()
		return nil, err
	}
	return eng, nil
}

func (e *Engine) initDetector() error {
	inputs, outputs, err := ort.GetInputOutputInfo(e.cfg.DetectorONNX)
	if err != nil {
		return fmt.Errorf("detector metadata: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		return fmt.Errorf("detector model has no inputs/outputs")
	}
	e.detInName = inputs[0].Name
	e.detOutName = outputs[0].Name
	shape := inputs[0].Dimensions
	if len(shape) != 4 || shape[2] != shape[3] {
		return fmt.Errorf("detector expects square NCHW input, got %v", shape)
	}
	e.detSize = int(shape[2])
	n := shape[0] * shape[1] * shape[2] * shape[3]
	e.detIn, err = ort.NewEmptyTensor[float32](shape)
	if err != nil {
		return err
	}
	outShape := outputs[0].Dimensions
	if len(outShape) == 3 {
		outShape = []int64{outShape[0] * outShape[1], outShape[2]}
	}
	if len(outShape) != 2 {
		return fmt.Errorf("unexpected detector output shape %v", outputs[0].Dimensions)
	}
	e.detOut, err = ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return err
	}
	_ = n
	e.detSess, err = ort.NewAdvancedSession(
		e.cfg.DetectorONNX,
		[]string{e.detInName},
		[]string{e.detOutName},
		[]ort.Value{e.detIn},
		[]ort.Value{e.detOut},
		nil,
	)
	return err
}

func (e *Engine) initOCR() error {
	inputs, outputs, err := ort.GetInputOutputInfo(e.cfg.OCRONNX)
	if err != nil {
		return fmt.Errorf("ocr metadata: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		return fmt.Errorf("ocr model has no inputs/outputs")
	}
	e.ocrInName = inputs[0].Name
	e.ocrOutName = outputs[0].Name
	h := int64(e.plateCfg.ImgHeight)
	w := int64(e.plateCfg.ImgWidth)
	ch := int64(3)
	if e.plateCfg.ImageColorMode == "grayscale" {
		ch = 1
	}
	inShape := ort.NewShape(1, h, w, ch)
	e.ocrIn, err = ort.NewEmptyTensor[uint8](inShape)
	if err != nil {
		return err
	}
	outShape := outputs[0].Dimensions
	e.ocrOut, err = ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return err
	}
	e.ocrSess, err = ort.NewAdvancedSession(
		e.cfg.OCRONNX,
		[]string{e.ocrInName},
		[]string{e.ocrOutName},
		[]ort.Value{e.ocrIn},
		[]ort.Value{e.ocrOut},
		nil,
	)
	return err
}

func (e *Engine) close() {
	if e.detSess != nil {
		e.detSess.Destroy()
	}
	if e.ocrSess != nil {
		e.ocrSess.Destroy()
	}
	if e.detIn != nil {
		e.detIn.Destroy()
	}
	if e.detOut != nil {
		e.detOut.Destroy()
	}
	if e.ocrIn != nil {
		e.ocrIn.Destroy()
	}
	if e.ocrOut != nil {
		e.ocrOut.Destroy()
	}
}

// Recognize detects a plate region then runs OCR on the crop.
func (e *Engine) Recognize(jpeg []byte) (Result, error) {
	im, err := decodeJPEG(jpeg)
	if err != nil {
		return Result{}, err
	}
	box := [4]int{}
	crop := im
	tensor, ratio, padW, padH := letterboxRGB(im, e.detSize)
	copy(e.detIn.GetData(), tensor)
	if err := e.detSess.Run(); err != nil {
		return Result{}, fmt.Errorf("detector run: %w", err)
	}
	outShape := e.detOut.GetShape()
	rows := int(outShape[0])
	cols := int(outShape[1])
	dets := decodeYoloV9End2End(e.detOut.GetData(), rows, cols, ratio, padW, padH, []string{"License Plate"}, e.cfg.DetConf)
	if best, ok := bestDetection(dets); ok {
		box = [4]int{best.x1, best.y1, best.x2, best.y2}
		crop = im.crop(best.x1, best.y1, best.x2, best.y2)
	} else {
		crop, box = heuristicPlateCrop(im)
	}
	if crop.w == 0 || crop.h == 0 {
		return Result{}, nil
	}
	plateBytes := plateTensorUint8(crop, e.plateCfg)
	inData := e.ocrIn.GetData()
	if len(plateBytes) != len(inData) {
		return Result{}, fmt.Errorf("ocr tensor size mismatch: got %d want %d", len(plateBytes), len(inData))
	}
	copy(inData, plateBytes)
	if err := e.ocrSess.Run(); err != nil {
		return Result{}, fmt.Errorf("ocr run: %w", err)
	}
	plate, conf := decodePlateOCR(e.ocrOut.GetData(), e.plateCfg)
	if plate == "" || conf < e.cfg.MinConf {
		return Result{Box: box}, nil
	}
	return Result{Plate: plate, Confidence: conf, Box: box}, nil
}
