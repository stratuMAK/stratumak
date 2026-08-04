// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"testing"
)

// The oracle for these cases is the C tool-table parser this replaces
// (src/emc/sai/sai_tooltable.cc parse_tool_line, itself derived from 2.9's
// tooldata.cc): keys are matched through toupper(), and every sscanf is
// checked with `if (!valid) return -1`, i.e. one malformed field rejects the
// whole line.

func TestParseTblLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want tblEntry
	}{
		{
			name: "full entry",
			line: "T1 P1 X1.5 Y2.5 Z-3.5 A10 B20 C30 U0.1 V0.2 W0.3 D6.35 I45 J-45 Q3 ;end mill",
			want: tblEntry{
				toolno: 1, pocketno: 1,
				x: 1.5, y: 2.5, z: -3.5, a: 10, b: 20, c: 30,
				u: 0.1, v: 0.2, w: 0.3,
				diameter: 6.35, frontangle: 45, backangle: -45,
				orientation: 3, comment: "end mill",
			},
		},
		{
			name: "minimal entry",
			line: "T5 P5",
			want: tblEntry{toolno: 5, pocketno: 5},
		},
		{
			// The C matches with toupper(token[0]), so a lowercase table loads.
			// Matching only uppercase silently dropped every field and then
			// rejected the line for having no tool number.
			name: "lowercase keys",
			line: "t7 p7 z-1.25 d3",
			want: tblEntry{toolno: 7, pocketno: 7, z: -1.25, diameter: 3},
		},
		{
			name: "mixed case keys",
			line: "T8 p8 Z-2 d4",
			want: tblEntry{toolno: 8, pocketno: 8, z: -2, diameter: 4},
		},
		{
			// T0 with a pocket is the random-toolchanger empty-pocket marker.
			name: "empty pocket marker",
			line: "T0 P3",
			want: tblEntry{toolno: 0, pocketno: 3},
		},
		{
			name: "comment only after semicolon",
			line: "T2 P2 ; a comment ; with another semicolon",
			want: tblEntry{toolno: 2, pocketno: 2, comment: "a comment ; with another semicolon"},
		},
		{
			name: "unknown keys ignored",
			line: "T3 P3 Z1 K99 M42",
			want: tblEntry{toolno: 3, pocketno: 3, z: 1},
		},
		{
			name: "tabs and repeated spaces",
			line: "T4\tP4   Z2",
			want: tblEntry{toolno: 4, pocketno: 4, z: 2},
		},
		{
			// Later wins, as in the C (each sscanf overwrites).
			name: "duplicate key",
			line: "T6 P6 Z1 Z2",
			want: tblEntry{toolno: 6, pocketno: 6, z: 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTblLine(tc.line)
			if err != nil {
				t.Fatalf("parseTblLine(%q): %v", tc.line, err)
			}
			if got != tc.want {
				t.Errorf("parseTblLine(%q)\n got %+v\nwant %+v", tc.line, got, tc.want)
			}
		})
	}
}

// TestParseTblLineRejects pins that a malformed field fails the line instead of
// defaulting to zero. This is the important one: the offsets used to be parsed
// with the error discarded, so "Z abc" became a Z offset of 0.0 — and a zeroed
// tool-length offset drives the tool into the work.
func TestParseTblLineRejects(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"no tool number", "P1 X1"},
		{"empty", ""},
		{"comment only", "; just a comment"},
		{"bad tool number", "Tx P1"},
		{"bad pocket number", "T1 Px"},
		{"bad orientation", "T1 P1 Qx"},
		{"bad x offset", "T1 P1 Xabc"},
		{"bad z offset", "T1 P1 Zabc"},
		{"bad diameter", "T1 P1 D--3"},
		{"bad frontangle", "T1 P1 I1.2.3"},
		{"tool number overflows int32", "T99999999999 P1"},
		{"bare key with no value", "T1 P1 Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTblLine(tc.line); err == nil {
				t.Errorf("parseTblLine(%q) succeeded, want a rejection", tc.line)
			}
		})
	}
}

// TestParseTblLinePartialFieldsRejected is the direct statement of the
// regression. Before, only T and P were checked; every offset used the
// discarded-error form, so the fields before the bad one were kept, the bad one
// silently became 0.0, and the line was accepted as a valid tool.
func TestParseTblLinePartialFieldsRejected(t *testing.T) {
	// Z parses, D does not. The line must be rejected outright — not accepted
	// with the good Z and a zeroed diameter.
	if got, err := parseTblLine("T1 P1 Z-2.5 Dnonsense"); err == nil {
		t.Fatalf("a line with a malformed diameter was accepted as %+v", got)
	}
	// And the good-field-after-bad-field ordering must not rescue it either.
	if got, err := parseTblLine("T1 P1 Dnonsense Z-2.5"); err == nil {
		t.Fatalf("a line with a malformed diameter was accepted as %+v", got)
	}
}
