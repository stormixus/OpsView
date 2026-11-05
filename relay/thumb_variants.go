package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
)

const cardThumbWidth = 512 // covers a ~256 CSS-pixel card at DPR 2

type thumbVariantJob struct {
	path string
	jpeg []byte
	done chan struct{}
}
type thumbVariantQueue struct {
	once    sync.Once
	mu      sync.Mutex
	pending map[string]chan struct{}
	jobs    chan thumbVariantJob
}

// queueCardThumb converts once in a bounded background pool. Requests never wait
// on an unbounded decoder/process queue. Content-addressed filenames keep a late
// replacement's old job from overwriting the new snapshot's derivative.
func (h *Hub) queueCardThumb(path string, data []byte) <-chan struct{} {
	if len(data) > maxEventThumbBytes {
		return nil
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || (cfg.Width <= cardThumbWidth && cfg.Height <= cardThumbWidth) || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return nil
	}
	select {
	case <-h.done:
		return nil
	default:
	}
	q := &h.thumbVariants
	q.once.Do(func() {
		q.pending = make(map[string]chan struct{})
		q.jobs = make(chan thumbVariantJob, 16)
		for i := 0; i < 2; i++ {
			go func() {
				for {
					select {
					case <-h.done:
						return
					case job := <-q.jobs:
						if img, err := makeCardThumb(job.jpeg); err == nil && len(img) > 0 {
							if f, err := os.CreateTemp(filepath.Dir(job.path), ".card-*"); err == nil {
								tmp := f.Name()
								_, writeErr := f.Write(img)
								closeErr := f.Close()
								if writeErr == nil && closeErr == nil {
									if err := os.Rename(tmp, job.path); err == nil {
										if info, err := os.Stat(job.path); err == nil {
											h.thumbnails.put(job.path, img, info)
										}
									}
								}
								os.Remove(tmp)
							}
						}
						q.mu.Lock()
						delete(q.pending, job.path)
						close(job.done)
						q.mu.Unlock()
					}
				}
			}()
		}
	})
	q.mu.Lock()
	defer q.mu.Unlock()
	if done := q.pending[path]; done != nil {
		return done
	}
	if len(q.jobs) >= cap(q.jobs) {
		return nil
	}
	done := make(chan struct{})
	job := thumbVariantJob{path: path, jpeg: append([]byte(nil), data...), done: done}
	select {
	case q.jobs <- job:
		q.pending[path] = done
		return done
	default:
		return nil
	}
}

func cardThumbPath(original string, data []byte) string {
	sum := sha256.Sum256(data)
	ext := filepath.Ext(original)
	return original[:len(original)-len(ext)] + fmt.Sprintf(".card.%x.jpg", sum)
}

// JPEG uses the existing browser/Go codec path. Only the derivative is resized;
// the agent's original JPEG remains untouched for inspection and evidence.
func makeCardThumb(data []byte) ([]byte, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return nil, fmt.Errorf("image dimensions exceed bound")
	}
	if cfg.Width <= cardThumbWidth && cfg.Height <= cardThumbWidth {
		return data, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	width := min(cfg.Width, cardThumbWidth)
	height := cfg.Height * width / cfg.Width
	if height > cardThumbWidth {
		height = cardThumbWidth
		width = max(1, cfg.Width*height/cfg.Height)
	}
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// Bilinear sampling is sufficient for a small camera preview and avoids a
	// runtime ffmpeg process or a new codec dependency per snapshot.
	for y := 0; y < height; y++ {
		sy := float64(y) * float64(cfg.Height-1) / float64(max(1, height-1))
		y0 := int(sy)
		y1 := min(y0+1, cfg.Height-1)
		fy := sy - float64(y0)
		for x := 0; x < width; x++ {
			sx := float64(x) * float64(cfg.Width-1) / float64(max(1, width-1))
			x0 := int(sx)
			x1 := min(x0+1, cfg.Width-1)
			fx := sx - float64(x0)
			a := color.RGBAModel.Convert(src.At(x0, y0)).(color.RGBA)
			b := color.RGBAModel.Convert(src.At(x1, y0)).(color.RGBA)
			c := color.RGBAModel.Convert(src.At(x0, y1)).(color.RGBA)
			d := color.RGBAModel.Convert(src.At(x1, y1)).(color.RGBA)
			blend := func(a, b, c, d uint8) uint8 {
				return uint8((float64(a)*(1-fx)+float64(b)*fx)*(1-fy) + (float64(c)*(1-fx)+float64(d)*fx)*fy + 0.5)
			}
			dst.SetRGBA(x, y, color.RGBA{R: blend(a.R, b.R, c.R, d.R), G: blend(a.G, b.G, c.G, d.G), B: blend(a.B, b.B, c.B, d.B), A: 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 60}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
