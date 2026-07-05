package bliss

import "testing"

func TestApplyEvent(t *testing.T) {
	// A status event updates position, reversed-flag and battery.
	s := applyEvent(State{}, Event{Type: EventStatus, Position: 42, Reversed: true, HasBattery: true, Battery: BatteryLow})
	if s.Position != 42 || !s.Reversed || s.Battery != BatteryLow {
		t.Fatalf("status apply = %+v", s)
	}

	// A status event without battery info must not clobber a known battery level.
	s = applyEvent(s, Event{Type: EventStatus, Position: 10, HasBattery: false})
	if s.Position != 10 {
		t.Errorf("position not updated: %+v", s)
	}
	if s.Battery != BatteryLow {
		t.Errorf("battery should be preserved when HasBattery is false: %+v", s)
	}

	// Login result flips LoggedIn without touching position.
	s = applyEvent(s, Event{Type: EventLoginResult, Success: true})
	if !s.LoggedIn || s.Position != 10 {
		t.Errorf("login apply = %+v", s)
	}

	// Unknown/other events leave state unchanged.
	before := s
	if got := applyEvent(s, Event{Type: EventTimerSet, Success: true}); got != before {
		t.Errorf("timer event should not mutate state: %+v", got)
	}
}
