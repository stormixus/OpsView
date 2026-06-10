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
	detSess     *ort.DynamicAdvancedSession
	ocrSess     *ort.DynamicAdvancedSession
	detSize     int
	ocrOutCount int

	// mu serializes Recognize: one shared Engine is invoked from per-event
	// goroutines, and onnxruntime DynamicAdvancedSession.Run is not guaranteed
	// safe for concurrent calls on the same session.
	mu sync.Mutex
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
	shape := inputs[0].Dimensions
	if len(shape) != 4 || shape[2] != shape[3] || shape[2] <= 0 {
		return fmt.Errorf("detector expects square NCHW input, got %v", shape)
	}
	e.detSize = int(shape[2])
	e.detSess, err = ort.NewDynamicAdvancedSession(
		e.cfg.DetectorONNX,
		[]string{inputs[0].Name},
		[]string{outputs[0].Name},
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
	outNames := make([]string, len(outputs))
	for i, o := range outputs {
		outNames[i] = o.Name
	}
	e.ocrOutCount = len(outputs)
	e.ocrSess, err = ort.NewDynamicAdvancedSession(
		e.cfg.OCRONNX,
		[]string{inputs[0].Name},
		outNames,
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
}

func destroyValues(vs []ort.Value) {
	for _, v := range vs {
		if v != nil {
			v.Destroy()
		}
	}
}

func tensorRowsCols(shape ort.Shape) (rows, cols int, err error) {
	switch len(shape) {
	case 2:
		return int(shape[0]), int(shape[1]), nil
	case 3:
		return int(shape[0] * shape[1]), int(shape[2]), nil
	default:
		return 0, 0, fmt.Errorf("unexpected output shape %v", shape)
	}
}

// Recognize detects a plate region then runs OCR on the crop. Serialized: the
// detector/OCR sessions are shared and onnxruntime Run isn't concurrency-safe.
func (e *Engine) Recognize(jpeg []byte) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	im, err := decodeJPEG(jpeg)
	if err != nil {
		return Result{}, err
	}
	box := [4]int{}
	crop := im

	tensor, ratio, padW, padH := letterboxRGB(im, e.detSize)
	detIn, err := ort.NewTensor(ort.NewShape(1, 3, int64(e.detSize), int64(e.detSize)), tensor)
	if err != nil {
		return Result{}, fmt.Errorf("detector input: %w", err)
	}
	detOuts := []ort.Value{nil}
	if err := e.detSess.Run([]ort.Value{detIn}, detOuts); err != nil {
		detIn.Destroy()
		return Result{}, fmt.Errorf("detector run: %w", err)
	}
	detIn.Destroy()
	defer detOuts[0].Destroy()

	detOut := detOuts[0].(*ort.Tensor[float32])
	rows, cols, err := tensorRowsCols(detOut.GetShape())
	if err != nil {
		return Result{}, err
	}
	dets := decodeYoloV9End2End(detOut.GetData(), rows, cols, ratio, padW, padH, []string{"License Plate"}, e.cfg.DetConf)
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
	h := int64(e.plateCfg.ImgHeight)
	w := int64(e.plateCfg.ImgWidth)
	ch := int64(3)
	if e.plateCfg.ImageColorMode == "grayscale" {
		ch = 1
	}
	ocrIn, err := ort.NewTensor(ort.NewShape(1, h, w, ch), plateBytes)
	if err != nil {
		return Result{}, fmt.Errorf("ocr input: %w", err)
	}
	ocrOuts := make([]ort.Value, e.ocrOutCount)
	for i := range ocrOuts {
		ocrOuts[i] = nil
	}
	if err := e.ocrSess.Run([]ort.Value{ocrIn}, ocrOuts); err != nil {
		ocrIn.Destroy()
		return Result{}, fmt.Errorf("ocr run: %w", err)
	}
	ocrIn.Destroy()
	defer destroyValues(ocrOuts)

	plateOut, ok := ocrOuts[0].(*ort.Tensor[float32])
	if !ok {
		return Result{}, fmt.Errorf("ocr plate output type %T", ocrOuts[0])
	}
	plate, conf := decodePlateOCR(plateOut.GetData(), e.plateCfg)
	if plate == "" || conf < e.cfg.MinConf {
		return Result{Box: box}, nil
	}
	return Result{Plate: plate, Confidence: conf, Box: box}, nil
}
