package main

import "testing"

func TestSettleTracker(t *testing.T) {
	// Travels, then holds at 40: settles only after two unchanged reads.
	tr := newSettleTracker()
	settledAt := -1
	for i, p := range []int{10, 20, 30, 40, 40, 40} {
		if tr.observe(p) {
			settledAt = i
			break
		}
	}
	if settledAt != 5 { // i=3 sets 40, i=4 stable=1, i=5 stable=2 -> settled
		t.Errorf("settled at index %d, want 5", settledAt)
	}

	// Starting from an end (0) and moving up must NOT settle while still moving.
	tr = newSettleTracker()
	for _, p := range []int{0, 5, 10} {
		if tr.observe(p) {
			t.Fatalf("must not settle while position (%d) is still changing", p)
		}
	}

	// A no-op (already at target) settles once the position repeats.
	tr = newSettleTracker()
	got := []bool{tr.observe(100), tr.observe(100), tr.observe(100)}
	if got[0] || got[1] || !got[2] {
		t.Errorf("no-op settle = %v, want [false false true]", got)
	}
}

func TestRestingState(t *testing.T) {
	cases := map[int]string{0: "closed", 100: "open", 50: "stopped", 1: "stopped", 99: "stopped"}
	for pos, want := range cases {
		if got := restingState(pos); got != want {
			t.Errorf("restingState(%d) = %q, want %q", pos, got, want)
		}
	}
}
