// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package comp

import (
	"strings"
	"testing"
)

// Backslash-newline inside a string is a line continuation (halcompile
// semantics): both characters vanish and the lines join. It used to become a
// literal newline, which put an empty first line into any triple-quoted
// summary opened as `component name """\` — and an empty Summary first line
// means a whatis-less NAME section in the generated man page.
func TestStringLineContinuation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"triple", "component c \"\"\"\\\nFirst line.\n\"\"\";\n;;\n", "First line."},
		{"triple-crlf", "component c \"\"\"\\\r\nFirst line.\n\"\"\";\n;;\n", "First line."},
		{"plain", "component c \"joined \\\nline\";\n;;\n", "joined line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg, err := Parse("c.comp", tc.src)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			got := strings.Split(pkg.Component.Summary, "\n")[0]
			if got != tc.want {
				t.Errorf("Summary first line = %q, want %q", got, tc.want)
			}
			if strings.HasPrefix(pkg.Component.Summary, "\n") {
				t.Errorf("Summary begins with a newline: %q", pkg.Component.Summary)
			}
		})
	}
}

// A raw triple-quoted string keeps its backslashes untouched, continuation
// included.
func TestRawTripleStringKeepsBackslash(t *testing.T) {
	pkg, err := Parse("c.comp", "component c r\"\"\"\\\nkept\"\"\";\n;;\n")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !strings.HasPrefix(pkg.Component.Summary, "\\\n") {
		t.Errorf("raw string lost its backslash-newline: %q", pkg.Component.Summary)
	}
}
