package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMosaicLayout(t *testing.T) {
	cases := []struct{ n, rows, cols int }{
		{1, 1, 1}, {2, 1, 2}, {3, 2, 2}, {4, 2, 2},
		{5, 2, 3}, {9, 3, 3}, {12, 3, 4}, {16, 4, 4},
	}
	for _, c := range cases {
		r, col := mosaicLayout(c.n)
		if r != c.rows || col != c.cols {
			t.Fatalf("mosaicLayout(%d) = %dx%d, want %dx%d", c.n, r, col, c.rows, c.cols)
		}
	}
	if r, c := mosaicLayout(0); r != 0 || c != 0 {
		t.Fatalf("mosaicLayout(0) = %d,%d, want 0,0", r, c)
	}
}

func TestMosaicInputIDs(t *testing.T) {
	stats := []StreamStat{
		{ID: "dvr1_ch10"}, {ID: "dvr1_ch2"}, {ID: "dvr1_ch2@main"},
		{ID: "dvr3_ch1"}, {ID: "wall"}, {ID: "dvr1_ch1"},
	}
	got := mosaicInputIDs(stats)
	want := []string{"dvr1_ch1", "dvr1_ch2", "dvr1_ch10", "dvr3_ch1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mosaicInputIDs = %v, want %v (numeric ch sort, no @main/wall)", got, want)
	}
}

func TestWallEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		os.Unsetenv("RELAY_WALL")
		os.Unsetenv("RELAY_WALL_RES")
		os.Unsetenv("RELAY_WALL_FPS")
		if wallEnabled() {
			t.Fatal("wall should be disabled by default")
		}
		if w, h := wallDims(); w != 1920 || h != 1080 {
			t.Fatalf("default dims = %dx%d, want 1920x1080", w, h)
		}
		if wallFPS() != 15 {
			t.Fatalf("default fps = %d, want 15", wallFPS())
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("RELAY_WALL", "1")
		t.Setenv("RELAY_WALL_RES", "720p")
		t.Setenv("RELAY_WALL_FPS", "10")
		if !wallEnabled() {
			t.Fatal("RELAY_WALL=1 should enable")
		}
		if w, h := wallDims(); w != 1280 || h != 720 {
			t.Fatalf("720p dims = %dx%d, want 1280x720", w, h)
		}
		if wallFPS() != 10 {
			t.Fatalf("fps = %d, want 10", wallFPS())
		}
	})
	t.Run("fps clamp", func(t *testing.T) {
		t.Setenv("RELAY_WALL_FPS", "999")
		if wallFPS() != 30 {
			t.Fatalf("fps clamp = %d, want 30", wallFPS())
		}
	})
}

func TestMosaicArgs(t *testing.T) {
	args := mosaicArgs([]string{"http://a/0.m3u8", "http://a/1.m3u8"}, 1, 2, 640, 360, 15)
	joined := strings.Join(args, " ")
	// both inputs present
	if !strings.Contains(joined, "-i http://a/0.m3u8") || !strings.Contains(joined, "-i http://a/1.m3u8") {
		t.Fatalf("missing inputs: %s", joined)
	}
	// per-input scale+pad+tpad and a 2-up xstack at x offsets 0 and 640
	if !strings.Contains(joined, "scale=640:360") || !strings.Contains(joined, "tpad=stop=-1:stop_mode=clone") {
		t.Fatalf("missing scale/tpad: %s", joined)
	}
	if !strings.Contains(joined, "xstack=inputs=2:layout=0_0|640_0") {
		t.Fatalf("missing/incorrect xstack layout: %s", joined)
	}
	// NVENC + Annex-B pipe + AUD bsf
	for _, want := range []string{"h264_nvenc", "h264_metadata=aud=insert", "-f h264 pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
}

func TestEvenDown(t *testing.T) {
	for in, want := range map[int]int{640: 640, 641: 640, 0: 0, 7: 6} {
		if got := evenDown(in); got != want {
			t.Fatalf("evenDown(%d) = %d, want %d", in, got, want)
		}
	}
}
