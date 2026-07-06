package main

import (
	"testing"

	"github.com/stretchr/testify/require"

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
		require.Equal(t, c.ok, ok, "parseDays(%q) ok", c.in)
		if c.ok {
			require.Equal(t, c.want, got, "parseDays(%q)", c.in)
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
		require.Equal(t, c.ok, ok, "parseHHMM(%q) ok", c.in)
		if c.ok {
			require.Equal(t, c.h, h, "parseHHMM(%q) hour", c.in)
			require.Equal(t, c.m, m, "parseHHMM(%q) min", c.in)
		}
	}
}

func TestAtoiOK(t *testing.T) {
	n, ok := atoiOK("42")
	require.True(t, ok)
	require.Equal(t, 42, n)

	_, ok = atoiOK("x")
	require.False(t, ok)
}
