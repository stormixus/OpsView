package main

import (
	"reflect"
	"testing"
)

func TestReorderByPreference(t *testing.T) {
	ids := []string{"dvr1_ch1", "dvr1_ch2", "dvr1_ch3", "dvr1_ch4"}
	// move ch3 and ch1 to the front; unlisted (ch2,ch4) keep their incoming order after
	got := reorderByPreference(ids, []string{"dvr1_ch3", "dvr1_ch1"})
	want := []string{"dvr1_ch3", "dvr1_ch1", "dvr1_ch2", "dvr1_ch4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder = %v, want %v", got, want)
	}
	// pref referencing a now-absent channel is ignored; empty pref = unchanged
	if g := reorderByPreference(ids, []string{"gone", "dvr1_ch2"}); !reflect.DeepEqual(g, []string{"dvr1_ch2", "dvr1_ch1", "dvr1_ch3", "dvr1_ch4"}) {
		t.Fatalf("absent-pref reorder = %v", g)
	}
	if g := reorderByPreference(ids, nil); !reflect.DeepEqual(g, ids) {
		t.Fatalf("empty pref should be unchanged, got %v", g)
	}
}
