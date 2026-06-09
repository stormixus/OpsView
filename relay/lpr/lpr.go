package lpr

import "log"

// Result is one license plate recognition outcome.
type Result struct {
	Plate      string
	Confidence float64
	Box        [4]int // x1,y1,x2,y2 in source image pixels
}

// Recognizer runs plate detection + OCR on a JPEG snapshot.
type Recognizer interface {
	Recognize(jpeg []byte) (Result, error)
}

// Disabled is a no-op recognizer.
type Disabled struct{}

func (Disabled) Recognize([]byte) (Result, error) { return Result{}, nil }

// FuncRecognizer adapts a function to Recognizer (tests, stubs).
type FuncRecognizer func(jpeg []byte) (Result, error)

func (f FuncRecognizer) Recognize(jpeg []byte) (Result, error) { return f(jpeg) }

// NewFromEnv builds the configured recognizer. When ONNX is unavailable or
// misconfigured, returns Disabled after logging once.
func NewFromEnv() Recognizer {
	cfg := ConfigFromEnv()
	if !cfg.Enabled {
		return Disabled{}
	}
	eng, err := newEngine(cfg)
	if err != nil {
		log.Printf("[lpr] disabled: %v", err)
		return Disabled{}
	}
	log.Printf("[lpr] ONNX engine ready (detector=%s ocr=%s)", cfg.DetectorONNX, cfg.OCRONNX)
	return eng
}
