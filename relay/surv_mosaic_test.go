package main

import (
	"reflect"
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
