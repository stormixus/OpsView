package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestSpriteCellTimes(t *testing.T) {
	got := spriteCellTimes(1000, 1100, 5) // 5 buckets of 20s, centers at +10
	want := []int64{1010, 1030, 1050, 1070, 1090}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if d := spriteCellTimes(100, 100, 5); d != nil {
		t.Fatalf("empty range should be nil, got %v", d)
	}
	if d := spriteCellTimes(0, 100, 0); d != nil {
		t.Fatalf("n=0 should be nil, got %v", d)
	}
}

func TestAssembleStrip(t *testing.T) {
	cw, ch := 160, 90
	red := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for i := range red.Pix {
		red.Pix[i] = 0xff
	}
	// 3 cells: red, nil(placeholder), red -> 160 x 270 strip, valid JPEG
	jpg, err := assembleStrip([]image.Image{red, nil, red}, cw, ch)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("decode strip: %v", err)
	}
	if b := img.Bounds(); b.Dx() != cw || b.Dy() != ch*3 {
		t.Fatalf("strip dims = %dx%d, want %dx%d", b.Dx(), b.Dy(), cw, ch*3)
	}
	// middle cell should be the dark placeholder (not white)
	r, g, bl, _ := img.At(cw/2, ch+ch/2).RGBA()
	if r>>8 > 60 || g>>8 > 60 || bl>>8 > 60 {
		t.Fatalf("placeholder cell too bright: %v", color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255})
	}
}
