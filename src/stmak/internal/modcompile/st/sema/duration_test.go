// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"strings"
	"testing"
)

// TestParseDuration covers the T# literal body.  The accepted cases include
// every spelling that appears in the three measured production projects, since
// those are what the parser exists to read.
func TestParseDuration(t *testing.T) {
	const (
		us = int64(1e3)
		ms = int64(1e6)
		s  = int64(1e9)
		m  = 60 * s
		h  = 60 * m
		d  = 24 * h
	)

	for _, tt := range []struct {
		body string
		want int64
	}{
		// From the projects, verbatim.
		{"0s", 0},
		{"0S", 0},
		{"3s", 3 * s},
		{"1000MS", 1000 * ms},
		{"5000MS", 5000 * ms},
		{"30s", 30 * s},
		{"500MS", 500 * ms},
		{"1s", s},

		// Compound, largest unit first.
		{"1m30s", m + 30*s},
		{"1d2h3m4s5ms", d + 2*h + 3*m + 4*s + 5*ms},
		{"2h30m", 2*h + 30*m},

		// The sub-millisecond units of ed.3, which nanoseconds make exact.
		{"1us", us},
		{"250ns", 250},
		{"1s500ms250us", s + 500*ms + 250*us},

		// Case and underscores are noise.
		{"1M30S", m + 30*s},
		{"1_000ms", 1000 * ms},

		// The first magnitude may overrun its unit: T#25h is a real duration
		// and means what it says.
		{"25h", 25 * h},
		{"90s", 90 * s},
		{"1500ms", 1500 * ms},

		// A fraction on the last magnitude.
		{"0.5s", 500 * ms},
		{"1m30.5s", m + 30*s + 500*ms},

		// Signed.
		{"-1s", -s},
		{"-1m30s", -(m + 30*s)},
		{"+2s", 2 * s},
	} {
		t.Run(tt.body, func(t *testing.T) {
			got, err := parseDuration(tt.body)
			if err != nil {
				t.Fatalf("T#%s: unexpected error: %v", tt.body, err)
			}
			if got != tt.want {
				t.Errorf("T#%s = %d ns, want %d", tt.body, got, tt.want)
			}
		})
	}
}

// TestParseDurationRejects covers the malformed bodies.  Each is refused for a
// stated reason rather than being read as something the author did not write.
func TestParseDurationRejects(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "needs a magnitude and a unit"},
		{"no unit", "500", "has no unit"},
		{"unknown unit", "5y", "is not a duration unit"},
		{"no magnitude", "ms", "is not a magnitude"},
		{"repeated unit", "1s1s", "none repeats"},
		{"units out of order", "30s1m", "largest to smallest"},
		// Only the first magnitude may overrun, or `1m90s` would be two
		// different durations depending on how you read it.
		{"later magnitude overruns", "1m90s", "overruns its unit"},
		{"fraction not last", "1.5m30s", "only the last magnitude"},
		{"overflow", "99999999999d", "too large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.body)
			if err == nil {
				t.Fatalf("T#%s parsed as %d ns; expected a diagnostic", tt.body, got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("T#%s: error is %q, want one containing %q", tt.body, err, tt.want)
			}
		})
	}
}

// TestDurationMillisecondBridge pins the one number that a ported CODESYS
// source depends on.
//
// CODESYS holds a TIME as milliseconds, so every IEC-named conversion has to
// agree with it or a ported computation is silently a thousand times off.
// stratuMAK holds nanoseconds, and the millisecond factor is what reconciles
// the two.
func TestDurationMillisecondBridge(t *testing.T) {
	ns, err := parseDuration("1s")
	if err != nil {
		t.Fatal(err)
	}
	if got := ns / int64(nsPerMillisecond); got != 1000 {
		t.Errorf("TIME_TO_DINT(T#1s) would be %d, want 1000 as CODESYS gives", got)
	}

	// And the seconds pair, which is deliberately not an IEC name so that no
	// CODESYS source can contain it and be read the other way.
	for _, name := range []string{"TIME_TO_SECONDS", "SECONDS_TO_TIME"} {
		conv, ok := timeConversions[name]
		if !ok {
			t.Fatalf("%s is missing", name)
		}
		if conv.unit != 1e9 {
			t.Errorf("%s scales by %v, want 1e9 -- it is the seconds bridge", name, conv.unit)
		}
	}
	for name, conv := range timeConversions {
		if strings.Contains(name, "SECONDS") {
			continue
		}
		if strings.Contains(name, "DT") {
			// A point in time converts in seconds, which is the unit CODESYS
			// holds a DT in.  There is no millisecond form of it to disagree
			// with.
			if conv.unit != nsPerSecond {
				t.Errorf("%s scales by %v, want seconds", name, conv.unit)
			}
			continue
		}
		if conv.unit != nsPerMillisecond {
			t.Errorf("%s scales by %v; every IEC-named conversion must be milliseconds "+
				"to agree with CODESYS", name, conv.unit)
		}
	}
}
