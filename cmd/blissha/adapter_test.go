package main

import "testing"

func TestResolveHCI(t *testing.T) {
	if got, err := resolveHCI(""); err != nil || got != "hci0" {
		t.Errorf(`resolveHCI("") = %q, %v; want hci0`, got, err)
	}
	if got, err := resolveHCI("hci1"); err != nil || got != "hci1" {
		t.Errorf("resolveHCI(hci1) = %q, %v; want hci1", got, err)
	}
	if got, err := resolveHCI("  hci2  "); err != nil || got != "hci2" {
		t.Errorf("resolveHCI should trim: %q, %v", got, err)
	}
	// A MAC that matches no adapter must error (rather than silently defaulting).
	if _, err := resolveHCI("00:00:00:00:00:99"); err == nil {
		t.Error("expected error for unknown adapter MAC")
	}
}
