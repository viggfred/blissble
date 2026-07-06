package blissha

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettleTracker(t *testing.T) {
	// Travels, then holds at 40: settles only after two unchanged reads.
	// (i=3 sets 40, i=4 stable=1, i=5 stable=2 -> settled.)
	tr := newSettleTracker()
	settledAt := -1
	for i, p := range []int{10, 20, 30, 40, 40, 40} {
		if tr.observe(p) {
			settledAt = i
			break
		}
	}
	require.Equal(t, 5, settledAt, "should settle only after two unchanged reads")

	// Starting from an end (0) and moving up must NOT settle while still moving.
	tr = newSettleTracker()
	for _, p := range []int{0, 5, 10} {
		require.False(t, tr.observe(p), "must not settle while position %d is still changing", p)
	}

	// A no-op (already at target) settles once the position repeats.
	tr = newSettleTracker()
	require.False(t, tr.observe(100))
	require.False(t, tr.observe(100))
	require.True(t, tr.observe(100))
}

func TestRestingState(t *testing.T) {
	cases := map[int]string{0: "closed", 100: "open", 50: "stopped", 1: "stopped", 99: "stopped"}
	for pos, want := range cases {
		require.Equal(t, want, restingState(pos), "restingState(%d)", pos)
	}
}
