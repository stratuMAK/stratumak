// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pnptask

import (
	"strings"
	"testing"
)

func TestParseDirMode(t *testing.T) {
	cases := []struct {
		in   string
		want DirMode
	}{
		{"", defaultDirMode},
		{"C+R+", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true}},
		{"C+R+~", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: true, Meander: true}},
		{"R-C+", DirMode{Primary: AxisRow, PrimaryUp: false, SecondaryUp: true}},
		{"C-R-~", DirMode{Primary: AxisCol, PrimaryUp: false, SecondaryUp: false, Meander: true}},
		// Case and surrounding whitespace are the INI's business, not the
		// operator's.
		{" c+r- ", DirMode{Primary: AxisCol, PrimaryUp: true, SecondaryUp: false}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDirMode(tc.in)
			if err != nil {
				t.Fatalf("parseDirMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseDirMode(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			if got.Secondary() == got.Primary {
				t.Errorf("Secondary() = %v, must differ from Primary %v", got.Secondary(), got.Primary)
			}
			// The rendered form has to parse back to the same mode, which is
			// what makes String() usable in a diagnostic.
			back, err := parseDirMode(got.String())
			if err != nil || back != got {
				t.Errorf("round trip of %q via %q: %+v, %v", tc.in, got.String(), back, err)
			}
		})
	}
}

func TestParseDirModeErrors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"C+C-", "given twice"},
		{"X+R+", "unknown axis"},
		{"C*R+", "unknown direction"},
		{"C+", "expected an axis token"},
		{"C+R+~~", "trailing"},
		{"C+R+X", "trailing"},
		{"CR", "unknown direction"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := parseDirMode(tc.in)
			if err == nil {
				t.Fatalf("parseDirMode(%q) succeeded, want an error containing %q", tc.in, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseDirMode(%q) error = %v, want it to contain %q", tc.in, err, tc.want)
			}
		})
	}
}
