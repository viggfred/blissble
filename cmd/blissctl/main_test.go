package main

import (
	"testing"

	"github.com/viggfred/blissble/pkg/bliss"
)

func TestParseDays(t *testing.T) {
	cases := []struct {
		in   string
		want bliss.Days
		ok   bool
	}{
		{"daily", bliss.EveryDay, true},
		{"weekdays", bliss.Weekdays, true},
		{"weekend", bliss.Weekend, true},
		{"mon", bliss.Monday, true},
		{"mon,wed,fri", bliss.Monday | bliss.Wednesday | bliss.Friday, true},
		{"MON,Tue", bliss.Monday | bliss.Tuesday, true}, // case-insensitive
		{"mon,funday", 0, false},                        // invalid token
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDays(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseDays(%q) = %#x,%v; want %#x,%v", c.in, byte(got), ok, byte(c.want), c.ok)
		}
	}
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		h, m int
		ok   bool
	}{
		{"07:30", 7, 30, true},
		{"23:59", 23, 59, true},
		{"00:00", 0, 0, true},
		{"24:00", 0, 0, false},
		{"12:60", 0, 0, false},
		{"7", 0, 0, false},
		{"aa:bb", 0, 0, false},
	}
	for _, c := range cases {
		h, m, ok := parseHHMM(c.in)
		if ok != c.ok || (ok && (h != c.h || m != c.m)) {
			t.Errorf("parseHHMM(%q) = %d,%d,%v; want %d,%d,%v", c.in, h, m, ok, c.h, c.m, c.ok)
		}
	}
}

func TestAtoiOK(t *testing.T) {
	if n, ok := atoiOK("42"); !ok || n != 42 {
		t.Errorf("atoiOK(42) = %d,%v", n, ok)
	}
	if _, ok := atoiOK("x"); ok {
		t.Errorf("atoiOK(x) should fail")
	}
}
