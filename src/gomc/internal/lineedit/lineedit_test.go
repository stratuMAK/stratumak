// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package lineedit

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeTerm stands in for a tty: raw mode is a no-op and the width is fixed, so
// the editor can be driven from an ordinary string of keystrokes.
type fakeTerm struct{ cols int }

func (f fakeTerm) MakeRaw() (func(), error) { return func() {}, nil }
func (f fakeTerm) Columns() int {
	if f.cols == 0 {
		return defaultColumns
	}
	return f.cols
}

// press feeds keys to an editor (pre-loaded with history) and returns the
// accepted line, everything written to the terminal, and the error.
func press(t *testing.T, keys string, history ...string) (string, string, error) {
	t.Helper()
	var out strings.Builder
	e := newEditor(strings.NewReader(keys), &out, fakeTerm{})
	for _, h := range history {
		e.AddHistory(h)
	}
	line, err := e.ReadLine("halcmd> ")
	return line, out.String(), err
}

const (
	up    = "\x1b[A"
	down  = "\x1b[B"
	left  = "\x1b[D"
	right = "\x1b[C"
	del   = "\x1b[3~"
	home  = "\x1b[H"
	end   = "\x1b[F"
	cr    = "\r"
)

func TestPlainLine(t *testing.T) {
	line, _, err := press(t, "newthread loop 1000000"+cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "newthread loop 1000000" {
		t.Fatalf("got %q", line)
	}
}

// The regression from issue #265: with no line editing the terminal handed the
// raw "\x1b[A" to the parser, which turned "1000000" into "1ß000000".  The
// editor must recall the previous command instead and never leak a byte of the
// escape sequence into the result.
func TestArrowUpRecallsHistoryAndLeaksNothing(t *testing.T) {
	line, _, err := press(t, up+cr, "newthread loop 1000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "newthread loop 1000000" {
		t.Fatalf("history recall got %q", line)
	}

	// Same keystrokes with an empty history: the sequence is swallowed, not
	// inserted.
	line, _, err = press(t, "1000"+up+down+cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "1000" {
		t.Fatalf("escape sequence leaked into the line: %q", line)
	}
}

func TestHistoryBrowsingKeepsTypedLine(t *testing.T) {
	// Type something, walk up through two entries, walk back down to it.
	line, _, err := press(t, "half-typed"+up+up+down+down+cr, "first", "second")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "half-typed" {
		t.Fatalf("got %q, want the line that was being typed back", line)
	}

	line, _, _ = press(t, up+up+cr, "first", "second")
	if line != "first" {
		t.Fatalf("two Ups from the newest entry: got %q, want %q", line, "first")
	}
}

func TestAddHistorySkipsBlanksAndRepeats(t *testing.T) {
	var out strings.Builder
	e := newEditor(strings.NewReader(""), &out, fakeTerm{})
	e.AddHistory("show pin")
	e.AddHistory("show pin")
	e.AddHistory("   ")
	e.AddHistory("")
	e.AddHistory("show sig")
	if got := e.History(); len(got) != 2 || got[0] != "show pin" || got[1] != "show sig" {
		t.Fatalf("history = %q", got)
	}
}

func TestUnknownEscapeSequencesAreSwallowed(t *testing.T) {
	// A mouse report, an SGR colour sequence, a bracketed-paste marker and a
	// bare ESC-x: none of them may reach the line.
	keys := "ab" + "\x1b[200~" + "\x1b[1;31m" + "\x1b[<0;12;7M" + "\x1bZ" + "cd" + cr
	line, _, err := press(t, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "abcd" {
		t.Fatalf("got %q, want %q", line, "abcd")
	}
}

func TestRawControlBytesAreDropped(t *testing.T) {
	line, _, _ := press(t, "lo\x00op\x07 1\x1f000"+cr)
	if line != "loop 1000" {
		t.Fatalf("got %q", line)
	}
}

func TestCursorMovementAndEditing(t *testing.T) {
	tests := []struct {
		name string
		keys string
		want string
	}{
		{"left inserts mid-line", "abd" + left + "c" + cr, "abcd"},
		{"right after left", "abd" + left + left + right + "x" + cr, "abxd"},
		{"backspace", "abcx\x7f" + cr, "abc"},
		{"delete key", "abc" + left + del + cr, "ab"},
		{"home and end", "bc" + home + "a" + end + "d" + cr, "abcd"},
		{"ctrl-a / ctrl-e", "bc\x01a\x05d" + cr, "abcd"},
		{"ctrl-k kills to end", "abcdef" + left + left + "\x0b" + cr, "abcd"},
		{"ctrl-u kills to start", "abcdef" + left + left + "\x15" + cr, "ef"},
		{"ctrl-w kills word", "setp foo.bar\x17baz" + cr, "setp baz"},
		{"ctrl-y yanks", "setp foo\x15gets \x19" + cr, "gets setp foo"},
		{"alt-b then insert", "loop 1000\x1bbX" + cr, "loop X1000"},
		{"ctrl-left is word-wise", "loop 1000\x1b[1;5DX" + cr, "loop X1000"},
		{"ctrl-t transposes", "ab\x14" + cr, "ba"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line, _, err := press(t, tc.keys)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if line != tc.want {
				t.Fatalf("got %q, want %q", line, tc.want)
			}
		})
	}
}

func TestCtrlCDiscardsLine(t *testing.T) {
	line, out, err := press(t, "half typed\x03")
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
	if line != "" {
		t.Fatalf("line = %q, want empty", line)
	}
	if !strings.Contains(out, "^C") {
		t.Fatalf("output did not echo ^C: %q", out)
	}
}

func TestCtrlDOnEmptyLineIsEOF(t *testing.T) {
	_, _, err := press(t, "\x04")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}

	// On a non-empty line Ctrl-D deletes the character under the cursor.
	line, _, err := press(t, "abXc"+left+left+"\x04"+cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "abc" {
		t.Fatalf("got %q", line)
	}
}

func TestEOFWithoutNewline(t *testing.T) {
	_, _, err := press(t, "show pin")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestCompletionSingleCandidate(t *testing.T) {
	var out strings.Builder
	e := newEditor(strings.NewReader("newt\t"+cr), &out, fakeTerm{})
	e.SetCompleter(wordCompleter([]string{"newthread", "delthread"}))
	line, err := e.ReadLine("halcmd> ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "newthread " {
		t.Fatalf("got %q", line)
	}
}

func TestCompletionCommonPrefixThenList(t *testing.T) {
	var out strings.Builder
	e := newEditor(strings.NewReader("n\t\t"+cr), &out, fakeTerm{})
	e.SetCompleter(wordCompleter([]string{"newthread", "newsig"}))
	line, err := e.ReadLine("halcmd> ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First TAB extends to the common prefix, the second lists the choices.
	if line != "new" {
		t.Fatalf("got %q, want the common prefix %q", line, "new")
	}
	if !strings.Contains(out.String(), "newthread") || !strings.Contains(out.String(), "newsig") {
		t.Fatalf("candidates were not listed: %q", out.String())
	}
}

func TestCompletionCompletesLastWordOnly(t *testing.T) {
	var out strings.Builder
	e := newEditor(strings.NewReader("show pi\t"+cr), &out, fakeTerm{})
	e.SetCompleter(wordCompleter([]string{"pin"}))
	line, err := e.ReadLine("halcmd> ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "show pin " {
		t.Fatalf("got %q", line)
	}
}

func TestCompletionIgnoresNonMatchingCandidates(t *testing.T) {
	var out strings.Builder
	e := newEditor(strings.NewReader("zz\t"+cr), &out, fakeTerm{})
	e.SetCompleter(wordCompleter([]string{"newthread"}))
	line, err := e.ReadLine("halcmd> ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "zz" {
		t.Fatalf("got %q, want the typed text untouched", line)
	}
}

// wordCompleter completes the last whitespace-separated word from a fixed set.
func wordCompleter(words []string) Completer {
	return func(head string) (int, []string) {
		start := strings.LastIndexAny(head, " \t") + 1
		var out []string
		for _, w := range words {
			if strings.HasPrefix(w, head[start:]) {
				out = append(out, w)
			}
		}
		return start, out
	}
}

func TestLongLineScrollsHorizontally(t *testing.T) {
	// Narrow terminal: the rendering must stay on one physical line, i.e. never
	// emit a newline while editing.
	var out strings.Builder
	e := newEditor(strings.NewReader(strings.Repeat("x", 100)+cr), &out, fakeTerm{cols: 20})
	line, err := e.ReadLine("halcmd> ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != strings.Repeat("x", 100) {
		t.Fatalf("line truncated: %d runes", len(line))
	}
	body := strings.TrimSuffix(out.String(), "\r\n")
	if strings.Contains(body, "\n") {
		t.Fatalf("editing emitted a newline: %q", body)
	}
}

// Ending a line while the cursor sits in the middle of it must first park the
// cursor at the end: "^C" or the newline written at a mid-line cursor would
// paint over text the operator can still see.
func TestLineIsFinishedAtItsEnd(t *testing.T) {
	// prompt "halcmd> " is 8 columns, the text 3 more.
	_, out, err := press(t, "abc"+left+left+"\x03")
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "\x1b[11C^C\r\n") {
		t.Fatalf("^C was not written at the end of the line: %q", out)
	}

	_, out, err = press(t, "abc"+left+left+cr)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(out, "\x1b[11C\r\n") {
		t.Fatalf("line was not finished at its end: %q", out)
	}
}
