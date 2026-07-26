// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"slices"
	"strings"
	"testing"
)

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

// completeHead is shared by bash's "complete -C" protocol and the interactive
// line editor's TAB key: it must report where the word being completed starts
// so the editor can replace exactly that word.
func TestCompleteHead(t *testing.T) {
	cases := []struct {
		head      string
		wantStart int
		want      []string // expected candidates (order-insensitive subset check)
		wantAll   bool     // want is the complete candidate list
	}{
		{head: "", wantStart: 0, want: []string{"show", "newthread"}},
		{head: "newt", wantStart: 0, want: []string{"newthread"}, wantAll: true},
		{head: "show ", wantStart: 5, want: showKeywords, wantAll: true},
		{head: "show pi", wantStart: 5, want: []string{"pin"}, wantAll: true},
		{head: "SHOW th", wantStart: 5, want: []string{"thread"}, wantAll: true},
		{head: "lock no", wantStart: 5, want: []string{"none"}, wantAll: true},
		{head: "newsig foo ", wantStart: 11, want: pinTypes, wantAll: true},
		// Leading options belong to halcmd, not to the subcommand, but the
		// offset must still point into the original string.
		{head: "-k newt", wantStart: 3, want: []string{"newthread"}, wantAll: true},
		{head: "-U http://h:1/ del", wantStart: 15, want: []string{"delsig", "delthread", "delf"}, wantAll: true},
	}
	for _, c := range cases {
		start, got := completeHead(c.head)
		if start != c.wantStart {
			t.Errorf("completeHead(%q) start = %d; want %d", c.head, start, c.wantStart)
		}
		if start >= 0 && start <= len(c.head) {
			// Every candidate must extend the word the offset points at.
			for _, g := range got {
				if !strings.HasPrefix(g, c.head[start:]) {
					t.Errorf("completeHead(%q): candidate %q does not extend %q", c.head, g, c.head[start:])
				}
			}
		}
		if c.wantAll && len(got) != len(c.want) {
			t.Errorf("completeHead(%q) = %v; want %v", c.head, got, c.want)
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("completeHead(%q) = %v; missing %q", c.head, got, w)
			}
		}
	}
}

func TestLastWordStart(t *testing.T) {
	cases := map[string]int{
		"":               0,
		"show":           0,
		"show ":          5,
		"show pi":        5,
		"net a b c":      8,
		"load foo \"a b": 9,  // the quoted word starts at the quote
		"setp x 'a b' ":  13, // closed quote, then a separator
	}
	for in, want := range cases {
		if got := lastWordStart(in); got != want {
			t.Errorf("lastWordStart(%q) = %d; want %d", in, got, want)
		}
	}
}
