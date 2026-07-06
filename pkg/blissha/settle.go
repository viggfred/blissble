package blissha

// settleStableReads is how many consecutive unchanged position samples mark a
// moving blind as stopped.
const settleStableReads = 2

// settleTracker detects when a moving blind has stopped: the reported position
// must be unchanged for settleStableReads consecutive samples. It deliberately
// treats no particular value (e.g. an end stop) as "settled", so a movement that
// starts from an end is not mistaken for already-there — the bug that made an
// open-from-closed bounce straight back to closed.
type settleTracker struct {
	last   int
	stable int
}

func newSettleTracker() settleTracker { return settleTracker{last: -1} }

// observe records a position sample and reports whether the blind has settled.
func (s *settleTracker) observe(pos int) bool {
	if pos == s.last {
		s.stable++
		return s.stable >= settleStableReads
	}
	s.last, s.stable = pos, 0
	return false
}

// restingState maps a Home Assistant position (0..100) to a resting cover state.
func restingState(haPos int) string {
	switch {
	case haPos >= 100:
		return "open"
	case haPos <= 0:
		return "closed"
	default:
		return "stopped"
	}
}
