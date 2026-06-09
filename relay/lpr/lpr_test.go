package lpr

import "testing"

func TestDecodePlateOCR(t *testing.T) {
	cfg := PlateConfig{
		MaxPlateSlots: 4,
		Alphabet:      "0123_",
		PadChar:       "_",
	}
	// slot0->'1', slot1->'2', slot2->'3', slot3-> pad
	raw := []float32{
		0, 1, 0, 0, 0,
		0, 0, 1, 0, 0,
		0, 0, 0, 1, 0,
		0, 0, 0, 0, 1,
	}
	plate, conf := decodePlateOCR(raw, cfg)
	if plate != "123" {
		t.Fatalf("plate = %q want 123", plate)
	}
	if conf <= 0 {
		t.Fatalf("confidence = %v want > 0", conf)
	}
}

func TestDecodeYoloV9End2End(t *testing.T) {
	raw := []float32{
		0, 10, 20, 110, 60, 0, 0.9,
		0, 200, 200, 300, 300, 0, 0.2,
	}
	dets := decodeYoloV9End2End(raw, 2, 7, 1.0, 0, 0, nil, 0.25)
	if len(dets) != 1 {
		t.Fatalf("detections = %d want 1", len(dets))
	}
	if dets[0].x1 != 10 || dets[0].y1 != 20 || dets[0].x2 != 110 || dets[0].y2 != 60 {
		t.Fatalf("unexpected box: %+v", dets[0])
	}
}

func TestHeuristicPlateCrop(t *testing.T) {
	im := rgbImage{w: 100, h: 100, rgb: make([]byte, 100*100*3)}
	crop, box := heuristicPlateCrop(im)
	if crop.w != 60 || crop.h != 45 {
		t.Fatalf("crop size = %dx%d want 60x45", crop.w, crop.h)
	}
	if box[0] != 20 || box[1] != 45 || box[2] != 80 || box[3] != 90 {
		t.Fatalf("box = %v", box)
	}
}
