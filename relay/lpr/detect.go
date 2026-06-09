package lpr

import "math"

type detection struct {
	x1, y1, x2, y2 int
	conf             float32
}

// decodeYoloV9End2End converts open-image-models YOLOv9 end2end ONNX output
// back to boxes in original image coordinates.
func decodeYoloV9End2End(raw []float32, rows int, cols int, ratio float64, padW, padH float64, classLabels []string, scoreThreshold float32) []detection {
	if rows == 0 || cols < 7 {
		return nil
	}
	var out []detection
	for i := 0; i < rows; i++ {
		off := i * cols
		score := raw[off+6]
		if score < scoreThreshold {
			continue
		}
		x1 := (float64(raw[off+1]) - padW) / ratio
		y1 := (float64(raw[off+2]) - padH) / ratio
		x2 := (float64(raw[off+3]) - padW) / ratio
		y2 := (float64(raw[off+4]) - padH) / ratio
		_ = classLabels // single-class plate detector
		out = append(out, detection{
			x1: int(x1 + 0.5), y1: int(y1 + 0.5),
			x2: int(x2 + 0.5), y2: int(y2 + 0.5),
			conf: score,
		})
	}
	return out
}

func bestDetection(dets []detection) (detection, bool) {
	if len(dets) == 0 {
		return detection{}, false
	}
	best := dets[0]
	for _, d := range dets[1:] {
		if d.conf > best.conf {
			best = d
		}
	}
	return best, true
}

func meanConfidence(probs []float32) float64 {
	if len(probs) == 0 {
		return 0
	}
	var s float64
	for _, p := range probs {
		s += float64(p)
	}
	return s / float64(len(probs))
}

// decodePlateOCR maps fast-plate-ocr plate head output to text.
func decodePlateOCR(raw []float32, cfg PlateConfig) (string, float64) {
	vocab := len(cfg.Alphabet)
	slots := cfg.MaxPlateSlots
	if vocab == 0 || slots == 0 || len(raw) < slots*vocab {
		return "", 0
	}
	alpha := []byte(cfg.Alphabet)
	var b []byte
	var probs []float32
	for s := 0; s < slots; s++ {
		base := s * vocab
		best := 0
		maxLogit := raw[base]
		for j := 1; j < vocab; j++ {
			if raw[base+j] > maxLogit {
				maxLogit = raw[base+j]
				best = j
			}
		}
		var sum float64
		for j := 0; j < vocab; j++ {
			sum += math.Exp(float64(raw[base+j] - maxLogit))
		}
		p := float32(math.Exp(float64(raw[base+best]-maxLogit)) / sum)
		probs = append(probs, p)
		b = append(b, alpha[best])
	}
	plate := string(b)
	if cfg.PadChar != "" {
		plate = trimRightRunes(plate, cfg.PadChar)
	}
	return plate, meanConfidence(probs)
}

func trimRightRunes(s, cut string) string {
	for len(s) > 0 && string(s[len(s)-1]) == cut {
		s = s[:len(s)-1]
	}
	return s
}
