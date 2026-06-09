//go:build !onnx

package lpr

import "fmt"

func newEngine(cfg Config) (Recognizer, error) {
	if cfg.DetectorONNX == "" || cfg.OCRONNX == "" || cfg.OCRConfigYAML == "" {
		return nil, fmt.Errorf("RELAY_LPR=1 but relay was built without -tags onnx (set RELAY_LPR_DETECTOR, RELAY_LPR_OCR, RELAY_LPR_OCR_CONFIG and rebuild with -tags onnx)")
	}
	return nil, fmt.Errorf("relay built without -tags onnx; install ONNX Runtime and rebuild: go build -tags onnx")
}
