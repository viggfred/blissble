package bliss

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyEvent(t *testing.T) {
	// A status event updates position, reversed-flag and battery.
	s := applyEvent(State{}, Event{Type: EventStatus, Position: 42, Reversed: true, HasBattery: true, Battery: BatteryLow})
	require.EqualValues(t, 42, s.Position)
	require.True(t, s.Reversed)
	require.Equal(t, BatteryLow, s.Battery)

	// A status event without battery info must not clobber a known battery level.
	s = applyEvent(s, Event{Type: EventStatus, Position: 10, HasBattery: false})
	require.EqualValues(t, 10, s.Position)
	require.Equal(t, BatteryLow, s.Battery, "battery must be preserved when HasBattery is false")

	// Login result flips LoggedIn without touching position.
	s = applyEvent(s, Event{Type: EventLoginResult, Success: true})
	require.True(t, s.LoggedIn)
	require.EqualValues(t, 10, s.Position)

	// Unknown/other events leave state unchanged.
	before := s
	require.Equal(t, before, applyEvent(s, Event{Type: EventTimerSet, Success: true}), "unrelated event must not mutate state")
}
