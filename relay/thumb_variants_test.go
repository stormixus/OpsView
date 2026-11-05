package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func variantJPEG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var noise uint32 = 12345
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := fill
			if c == nil {
				noise = noise*1664525 + 1013904223
				c = color.RGBA{R: byte(noise >> 24), G: byte(noise >> 16), B: byte(noise >> 8), A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func newVariantHub(t *testing.T) *Hub {
	t.Helper()
	h := NewHub(cfgWithDash("variant-test-secret"))
	h.rec = &Recorder{dir: t.TempDir(), segSecs: 300}
	t.Cleanup(h.Stop)
	return h
}

func variantRequest(h *Hub, query string, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/rec-thumb?stream=cam&t=1000"+query, nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	response := httptest.NewRecorder()
	h.HandleDashboardRecThumb(response, req)
	return response
}

func waitVariant(t *testing.T, done <-chan struct{}) {
	t.Helper()
	if done == nil {
		t.Fatal("valid card job was not accepted")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("card conversion did not finish")
	}
}

func TestCardVariantDimensionsSizeAndSourcePreserved(t *testing.T) {
	original := variantJPEG(t, 704, 396, nil)
	before := append([]byte(nil), original...)
	result, err := makeCardThumb(original)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(result))
	if err != nil || cfg.Width != 512 || cfg.Height != 288 {
		t.Fatalf("card lost 16:9 dimensions: %+v, %v", cfg, err)
	}
	if len(result) >= len(original) {
		t.Fatalf("preview did not reduce bytes: %d >= %d", len(result), len(original))
	}
	if !bytes.Equal(before, original) {
		t.Fatal("conversion mutated the original JPEG")
	}
	portrait := variantJPEG(t, 704, 1056, color.White)
	result, err = makeCardThumb(portrait)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = jpeg.DecodeConfig(bytes.NewReader(result))
	if err != nil || cfg.Width != 341 || cfg.Height != 512 {
		t.Fatalf("portrait aspect ratio lost: %+v, %v", cfg, err)
	}
}

func TestCardVariantSkipsSmallInvalidAndOversizedInputs(t *testing.T) {
	h := newVariantHub(t)
	small := variantJPEG(t, 512, 288, color.White)
	unchanged, err := makeCardThumb(small)
	if err != nil || !bytes.Equal(small, unchanged) {
		t.Fatalf("small original should not be recompressed: %v", err)
	}
	largeDimensions := append([]byte(nil), small...)
	sof := bytes.Index(largeDimensions, []byte{0xff, 0xc0})
	if sof < 0 {
		t.Fatal("JPEG fixture lacks baseline SOF")
	}
	binary.BigEndian.PutUint16(largeDimensions[sof+5:sof+7], 4000)
	binary.BigEndian.PutUint16(largeDimensions[sof+7:sof+9], 5000)
	for _, data := range [][]byte{nil, []byte("not a JPEG"), small, make([]byte, maxEventThumbBytes+1), largeDimensions} {
		if done := h.queueCardThumb(filepath.Join(h.rec.dir, "unused.jpg"), data); done != nil {
			t.Fatalf("rejected image started conversion: %d bytes", len(data))
		}
	}
	if h.thumbVariants.jobs != nil {
		t.Fatal("invalid/small images initialized the worker queue")
	}
	if _, err := makeCardThumb([]byte("not a JPEG")); err == nil {
		t.Fatal("invalid image accepted")
	}
	if _, err := makeCardThumb(largeDimensions); err == nil {
		t.Fatal("decompression-bomb dimensions accepted")
	}
}

func TestCardVariantReplacementUsesIndependentContentKeys(t *testing.T) {
	h := newVariantHub(t)
	original := filepath.Join(h.rec.dir, "snapshot.jpg")
	red := variantJPEG(t, 704, 396, color.RGBA{R: 255, A: 255})
	blue := variantJPEG(t, 704, 396, color.RGBA{B: 255, A: 255})
	redPath, bluePath := cardThumbPath(original, red), cardThumbPath(original, blue)
	if redPath == bluePath || redPath == original || bluePath == original {
		t.Fatal("replacement shares a derivative or original path")
	}
	if err := os.WriteFile(original, blue, 0o600); err != nil {
		t.Fatal(err)
	}
	redDone := h.queueCardThumb(redPath, red)
	blueDone := h.queueCardThumb(bluePath, blue)
	waitVariant(t, redDone)
	waitVariant(t, blueDone)
	for _, sample := range []struct {
		path string
		red  bool
	}{{redPath, true}, {bluePath, false}} {
		data, err := os.ReadFile(sample.path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		pixel := color.RGBAModel.Convert(img.At(256, 144)).(color.RGBA)
		if (sample.red && pixel.R <= pixel.B) || (!sample.red && pixel.B <= pixel.R) {
			t.Fatalf("late old job overwrote another content's derivative: %+v", pixel)
		}
	}
	if raw, err := os.ReadFile(original); err != nil || !bytes.Equal(raw, blue) {
		t.Fatalf("derivative worker modified the replacement original: %v", err)
	}
}

// Hold consumers out of this producer test to make deduplication/backpressure
// deterministic. Other tests exercise the real background workers end-to-end.
func holdVariantWorkers(h *Hub) {
	h.thumbVariants.once.Do(func() {
		h.thumbVariants.pending = make(map[string]chan struct{})
		h.thumbVariants.jobs = make(chan thumbVariantJob, 16)
	})
}

func TestCardVariantConcurrentDedupCopiesInputAndBoundsQueue(t *testing.T) {
	h := newVariantHub(t)
	holdVariantWorkers(h)
	data := variantJPEG(t, 704, 396, color.White)
	before := append([]byte(nil), data...)
	path := cardThumbPath(filepath.Join(h.rec.dir, "one.jpg"), data)
	results := make(chan (<-chan struct{}), 64)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- h.queueCardThumb(path, data) }()
	}
	wg.Wait()
	close(results)
	unique := make(map[<-chan struct{}]bool)
	for done := range results {
		if done == nil {
			t.Fatal("duplicate caller rejected instead of sharing accepted work")
		}
		unique[done] = true
	}
	if len(unique) != 1 || len(h.thumbVariants.jobs) != 1 {
		t.Fatalf("concurrent duplicate jobs: %d completion channels, %d jobs", len(unique), len(h.thumbVariants.jobs))
	}
	data[len(data)-10] ^= 0xff
	job := <-h.thumbVariants.jobs
	if !bytes.Equal(job.jpeg, before) {
		t.Fatal("queued job aliases caller-owned JPEG bytes")
	}
	for i := 0; i < 16; i++ {
		if h.queueCardThumb(filepath.Join(h.rec.dir, fmt.Sprintf("job-%d.jpg", i)), before) == nil {
			t.Fatalf("queue rejected slot %d before capacity", i)
		}
	}
	if h.queueCardThumb(filepath.Join(h.rec.dir, "overflow.jpg"), before) != nil || len(h.thumbVariants.jobs) != 16 {
		t.Fatal("full queue accepted unbounded additional work")
	}
	h.Stop()
	if h.queueCardThumb(filepath.Join(h.rec.dir, "stopped.jpg"), before) != nil {
		t.Fatal("stopped hub accepted conversion work")
	}
}

func TestCardVariantHandlerEventuallyServesCardAndPreservesRaw(t *testing.T) {
	h := newVariantHub(t)
	original := variantJPEG(t, 704, 396, nil)
	h.storeEventThumb("cam", 1_000_000, original)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		response := variantRequest(h, "&size=card", ctx)
		if response.Code != http.StatusOK {
			t.Fatalf("card response: %d", response.Code)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Width == 512 && cfg.Height == 288 {
			break
		}
		if cfg.Width != 704 || cfg.Height != 396 {
			t.Fatalf("unexpected fallback dimensions: %+v", cfg)
		}
		select {
		case <-ctx.Done():
			t.Fatal("card variant never became available")
		case <-ticker.C:
		}
	}
	response := variantRequest(h, "", ctx)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) {
		t.Fatal("raw endpoint no longer returns the original 704px snapshot")
	}
	if data, err := os.ReadFile(evThumbPath(h.rec.dir, "cam", 1000)); err != nil || !bytes.Equal(data, original) {
		t.Fatalf("stored original changed: %v", err)
	}
}

func TestCardVariantHandlerFallsBackWhenConversionQueueStalls(t *testing.T) {
	h := newVariantHub(t)
	holdVariantWorkers(h)
	original := variantJPEG(t, 704, 396, color.White)
	h.storeEventThumb("cam", 1_000_000, original)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response := variantRequest(h, "&size=card", ctx)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) || ctx.Err() != nil {
		t.Fatal("stalled derivative blocked the handler instead of serving original")
	}
}

func TestExactFrameWithoutVideoDoesNotSubstituteEventSnapshot(t *testing.T) {
	h := newVariantHub(t)
	original := variantJPEG(t, 320, 180, color.White)
	h.storeEventThumb("cam", 1_000_000, original)
	for _, extra := range []string{"", "&t=1001"} {
		// Build the nearby request separately; duplicate t values would select the
		// first value and accidentally leave the nearest-snapshot path untested.
		query := "/dashboard/api/rec-thumb?stream=cam&t=1000"
		if extra != "" {
			query = "/dashboard/api/rec-thumb?stream=cam&t=1001"
		}
		request := func(frame bool) *httptest.ResponseRecorder {
			url := query
			if frame {
				url += "&frame=1"
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
			response := httptest.NewRecorder()
			h.HandleDashboardRecThumb(response, req)
			return response
		}
		if normal := request(false); normal.Code != http.StatusOK || !bytes.Equal(normal.Body.Bytes(), original) {
			t.Fatal("event snapshot fixture not available for normal thumbnail")
		}
		if exact := request(true); exact.Code != http.StatusNotFound {
			t.Fatalf("exact frame substituted wrong-time stored image: %d", exact.Code)
		}
	}
}

func TestExactFrameExtractsVideoInsteadOfStoredEventImage(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	h := newVariantHub(t)
	wrongSnapshot := variantJPEG(t, 320, 180, color.RGBA{R: 255, A: 255})
	start := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local)
	h.storeEventThumb("cam", start.UnixMilli(), wrongSnapshot)
	dir, _ := h.rec.safeStreamDir("cam")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output := filepath.Join(dir, start.Format(recNameLayout)+".mp4")
	command := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:size=160x96:rate=5",
		"-t", "2", "-c:v", "libx264", "-preset", "ultrafast", output)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate actual video: %v: %s", err, data)
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/api/rec-thumb?stream=cam&t=%d&frame=1", start.Unix()), nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	response := httptest.NewRecorder()
	h.HandleDashboardRecThumb(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("actual frame status: %d", response.Code)
	}
	img, err := jpeg.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	pixel := color.RGBAModel.Convert(img.At(100, 50)).(color.RGBA)
	if img.Bounds().Dx() != 320 || pixel.B <= pixel.R {
		t.Fatalf("exact preview is stored red event image instead of blue recording: bounds=%v pixel=%+v", img.Bounds(), pixel)
	}
}

func TestExactFrameWaitingForExtractorHonorsCancellation(t *testing.T) {
	h := newVariantHub(t)
	dir, _ := h.rec.safeStreamDir("cam")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local)
	if err := os.WriteFile(filepath.Join(dir, start.Format(recNameLayout)+".mp4"), []byte("not decoded while capacity is occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(thumbExtractSlots); i++ {
		thumbExtractSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(thumbExtractSlots); i++ {
			<-thumbExtractSlots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/api/rec-thumb?stream=cam&t=%d&frame=1", start.Unix()), nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	response := httptest.NewRecorder()
	h.HandleDashboardRecThumb(response, req)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("cancelled preview ignored bounded extractor wait: %d %q", response.Code, response.Body.String())
	}
}
