package lpr

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
)

// rgbImage is an HxW RGB byte slice (row-major).
type rgbImage struct {
	w, h int
	rgb  []byte // len = w*h*3
}

func decodeJPEG(data []byte) (rgbImage, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return rgbImage{}, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := rgbImage{w: w, h: h, rgb: make([]byte, w*h*3)}
	switch m := img.(type) {
	case *image.RGBA:
		copyRGB(out, m.Pix, m.Stride, b)
	case *image.NRGBA:
		copyRGBFromNRGBA(out, m.Pix, m.Stride, b)
	case *image.YCbCr:
		fillRGBFromYCbCr(out, m, b)
	default:
		// slow path
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r16, g16, b16, _ := m.At(x, y).RGBA()
				i := (y-b.Min.Y)*w + (x - b.Min.X)
				out.rgb[i*3] = byte(r16 >> 8)
				out.rgb[i*3+1] = byte(g16 >> 8)
				out.rgb[i*3+2] = byte(b16 >> 8)
			}
		}
	}
	return out, nil
}

func copyRGB(dst rgbImage, pix []byte, stride int, b image.Rectangle) {
	w, h := dst.w, dst.h
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := y*stride + x*4
			di := (y*w + x) * 3
			dst.rgb[di] = pix[si]
			dst.rgb[di+1] = pix[si+1]
			dst.rgb[di+2] = pix[si+2]
		}
	}
}

func copyRGBFromNRGBA(dst rgbImage, pix []byte, stride int, b image.Rectangle) {
	w, h := dst.w, dst.h
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := (y+b.Min.Y-b.Min.Y)*stride + (x+b.Min.X-b.Min.X)*4
			di := (y*w + x) * 3
			dst.rgb[di] = pix[si]
			dst.rgb[di+1] = pix[si+1]
			dst.rgb[di+2] = pix[si+2]
		}
	}
}

func fillRGBFromYCbCr(dst rgbImage, m *image.YCbCr, b image.Rectangle) {
	w, h := dst.w, dst.h
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r16, g16, b16, _ := m.At(x+b.Min.X, y+b.Min.Y).RGBA()
			di := (y*w + x) * 3
			dst.rgb[di] = byte(r16 >> 8)
			dst.rgb[di+1] = byte(g16 >> 8)
			dst.rgb[di+2] = byte(b16 >> 8)
		}
	}
}

func (im rgbImage) crop(x1, y1, x2, y2 int) rgbImage {
	x1 = clamp(x1, 0, im.w)
	y1 = clamp(y1, 0, im.h)
	x2 = clamp(x2, 0, im.w)
	y2 = clamp(y2, 0, im.h)
	if x2 <= x1 || y2 <= y1 {
		return rgbImage{}
	}
	cw, ch := x2-x1, y2-y1
	out := rgbImage{w: cw, h: ch, rgb: make([]byte, cw*ch*3)}
	for y := 0; y < ch; y++ {
		srcOff := ((y+y1)*im.w + x1) * 3
		dstOff := y * cw * 3
		copy(out.rgb[dstOff:dstOff+cw*3], im.rgb[srcOff:srcOff+cw*3])
	}
	return out
}

func heuristicPlateCrop(im rgbImage) (rgbImage, [4]int) {
	w, h := im.w, im.h
	box := [4]int{int(float64(w) * 0.2), int(float64(h) * 0.45), int(float64(w) * 0.8), int(float64(h) * 0.9)}
	return im.crop(box[0], box[1], box[2], box[3]), box
}

func letterboxRGB(im rgbImage, size int) (tensor []float32, ratio float64, padW, padH float64) {
	target := float64(size)
	shapeH, shapeW := float64(im.h), float64(im.w)
	r := math.Min(target/shapeH, target/shapeW)
	newW := int(math.Round(shapeW * r))
	newH := int(math.Round(shapeH * r))
	resized := resizeRGB(im, newW, newH)
	padW = (target - float64(newW)) / 2
	padH = (target - float64(newH)) / 2
	left := int(math.Round(padW - 0.1))
	top := int(math.Round(padH - 0.1))
	right := int(math.Round(padW + 0.1))
	bottom := int(math.Round(padH + 0.1))
	canvasW := newW + left + right
	canvasH := newH + top + bottom
	// NCHW float32 RGB 0-1
	tensor = make([]float32, 3*canvasW*canvasH)
	const pad byte = 114
	for y := 0; y < canvasH; y++ {
		for x := 0; x < canvasW; x++ {
			var r8, g8, b8 byte = pad, pad, pad
			if y >= top && y < top+newH && x >= left && x < left+newW {
				si := ((y-top)*newW + (x - left)) * 3
				r8, g8, b8 = resized.rgb[si], resized.rgb[si+1], resized.rgb[si+2]
			}
			// CHW layout
			idx := y*canvasW + x
			tensor[0*canvasH*canvasW+idx] = float32(r8) / 255
			tensor[1*canvasH*canvasW+idx] = float32(g8) / 255
			tensor[2*canvasH*canvasW+idx] = float32(b8) / 255
		}
	}
	return tensor, r, padW, padH
}

func resizeRGB(im rgbImage, nw, nh int) rgbImage {
	if nw <= 0 || nh <= 0 {
		return rgbImage{}
	}
	out := rgbImage{w: nw, h: nh, rgb: make([]byte, nw*nh*3)}
	for y := 0; y < nh; y++ {
		sy := float64(y) * float64(im.h-1) / float64(max(nh-1, 1))
		for x := 0; x < nw; x++ {
			sx := float64(x) * float64(im.w-1) / float64(max(nw-1, 1))
			r, g, b := bilinearSample(im, sx, sy)
			di := (y*nw + x) * 3
			out.rgb[di], out.rgb[di+1], out.rgb[di+2] = r, g, b
		}
	}
	return out
}

func resizePlateRGB(im rgbImage, cfg PlateConfig) rgbImage {
	mode := cfg.ImageColorMode
	if mode == "" {
		mode = "grayscale"
	}
	src := im
	if mode == "grayscale" {
		src = im.toGrayRGB()
	}
	if cfg.KeepAspectRatio {
		return letterboxPlate(src, cfg.ImgWidth, cfg.ImgHeight, mode)
	}
	return resizeRGB(src, cfg.ImgWidth, cfg.ImgHeight)
}

func (im rgbImage) toGrayRGB() rgbImage {
	out := rgbImage{w: im.w, h: im.h, rgb: make([]byte, len(im.rgb))}
	for i := 0; i < len(im.rgb); i += 3 {
		r, g, b := im.rgb[i], im.rgb[i+1], im.rgb[i+2]
		y := byte((19595*int(r) + 38470*int(g) + 7471*int(b) + 32768) >> 16)
		out.rgb[i], out.rgb[i+1], out.rgb[i+2] = y, y, y
	}
	return out
}

func letterboxPlate(im rgbImage, tw, th int, mode string) rgbImage {
	r := math.Min(float64(th)/float64(im.h), float64(tw)/float64(im.w))
	nw := int(math.Round(float64(im.w) * r))
	nh := int(math.Round(float64(im.h) * r))
	resized := resizeRGB(im, nw, nh)
	dw := (tw - nw) / 2
	dh := (th - nh) / 2
	out := rgbImage{w: tw, h: th, rgb: make([]byte, tw*th*3)}
	pad := byte(114)
	for i := range out.rgb {
		out.rgb[i] = pad
	}
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			di := ((y+dh)*tw + (x + dw)) * 3
			si := (y*nw + x) * 3
			out.rgb[di] = resized.rgb[si]
			out.rgb[di+1] = resized.rgb[si+1]
			out.rgb[di+2] = resized.rgb[si+2]
		}
	}
	_ = mode
	return out
}

func plateTensorUint8(im rgbImage, cfg PlateConfig) []uint8 {
	resized := resizePlateRGB(im, cfg)
	ch := 3
	if cfg.ImageColorMode == "grayscale" {
		ch = 1
	}
	out := make([]uint8, resized.h*resized.w*ch)
	if ch == 1 {
		for i := 0; i < resized.w*resized.h; i++ {
			out[i] = resized.rgb[i*3]
		}
		return out
	}
	copy(out, resized.rgb)
	return out
}

func bilinearSample(im rgbImage, fx, fy float64) (byte, byte, byte) {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := min(x0+1, im.w-1)
	y1 := min(y0+1, im.h-1)
	dx := fx - float64(x0)
	dy := fy - float64(y0)
	i00 := (y0*im.w + x0) * 3
	i10 := (y0*im.w + x1) * 3
	i01 := (y1*im.w + x0) * 3
	i11 := (y1*im.w + x1) * 3
	lerp := func(a, b float64, t float64) float64 { return a + (b-a)*t }
	sample := func(c int) float64 {
		v00 := float64(im.rgb[i00+c])
		v10 := float64(im.rgb[i10+c])
		v01 := float64(im.rgb[i01+c])
		v11 := float64(im.rgb[i11+c])
		top := lerp(v00, v10, dx)
		bot := lerp(v01, v11, dx)
		return lerp(top, bot, dy)
	}
	r := sample(0)
	g := sample(1)
	b := sample(2)
	return byte(r + 0.5), byte(g + 0.5), byte(b + 0.5)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
