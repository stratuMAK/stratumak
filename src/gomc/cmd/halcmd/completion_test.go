// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import "testing"

func TestParseCompPoint(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0", 0},
		{"11", 11},
		{"", 0},
		{"7x", 7},    // stop at first non-digit (atoi semantics)
		{"x7", 0},    // leading non-digit → 0, never negative
		{"-5", 0},    // '-' is not a digit → 0
		{"12 3", 12}, // stop at space
	}
	for _, c := range cases {
		if got := parseCompPoint(c.in); got != c.want {
			t.Errorf("parseCompPoint(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}

// A cursor positioned inside the command name (a mid-line TAB) reports an
// absolute offset smaller than the start of the argument region. relevantCompLine
// must clamp instead of panicking with a slice-bounds error.
func TestRelevantCompLine_MidLineTABDoesNotPanic(t *testing.T) {
	const line = "halcmd setp"
	for point := 0; point <= len(line)+5; point++ {
		got := relevantCompLine(line, point) // must not panic
		if point <= len("halcmd ") {
			if got != "" {
				t.Errorf("point=%d: got %q; want empty (cursor before arg region)", point, got)
			}
		}
	}
	// A normal end-of-line cursor yields the argument text.
	if got := relevantCompLine(line, len(line)); got != "setp" {
		t.Errorf("relevantCompLine(%q, %d) = %q; want %q", line, len(line), got, "setp")
	}
}

func TestRelevantCompLine_OverlongPointClamped(t *testing.T) {
	const line = "halcmd show pin"
	if got := relevantCompLine(line, 9999); got != "show pin" {
		t.Errorf("overlong point: got %q; want %q", got, "show pin")
	}
}
