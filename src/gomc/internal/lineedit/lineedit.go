// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package lineedit implements the interactive line editing that halcmd's
// prompt needs: cursor movement, emacs-style editing keys, command history and
// TAB completion.
//
// Without it a terminal in canonical mode hands the program whatever bytes the
// keyboard produced, so pressing Up at the prompt inserts the literal escape
// sequence "\x1b[A" into the command line — which is how "newthread loop
// 1000000" ends up being parsed as period "1ß000000" (issue #265).  The editor
// consumes escape sequences instead: known ones are turned into editing
// actions and unknown ones are swallowed, so no control byte can ever reach the
// command parser.
//
// The implementation is a self-contained linenoise-style editor (single visible
// line with horizontal scrolling) rather than a binding to GNU readline: it
// keeps halcmd cgo-free and adds no external module dependency.
package lineedit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInterrupted is returned by ReadLine when the user pressed Ctrl-C.  The
// partially typed line is discarded; the caller should simply prompt again.
var ErrInterrupted = errors.New("interrupted")

// ErrNotTerminal is returned by New when the input is not a tty, in which case
// the caller must fall back to plain line-at-a-time reading.
var ErrNotTerminal = errors.New("not a terminal")

const (
	defaultColumns = 80
	maxHistory     = 500
)

// Control characters we act on.  Named to keep the key dispatch readable.
const (
	keyCtrlA     = 1
	keyCtrlB     = 2
	keyCtrlC     = 3
	keyCtrlD     = 4
	keyCtrlE     = 5
	keyCtrlF     = 6
	keyCtrlH     = 8
	keyTab       = 9
	keyCtrlJ     = 10
	keyCtrlK     = 11
	keyCtrlL     = 12
	keyEnter     = 13
	keyCtrlN     = 14
	keyCtrlP     = 16
	keyCtrlT     = 20
	keyCtrlU     = 21
	keyCtrlW     = 23
	keyCtrlY     = 25
	keyEsc       = 27
	keyBackspace = 127
)

// Completer produces TAB-completion candidates for the text left of the
// cursor.  It returns the byte offset within head at which the word being
// completed starts, plus the candidate words (each a full replacement for that
// word).  Returning no candidates means "nothing to complete".
type Completer func(head string) (start int, candidates []string)

// termCtl abstracts the tty operations so the editor can be unit-tested
// against an ordinary pipe.
type termCtl interface {
	// MakeRaw puts the terminal into raw mode and returns a restore func.
	MakeRaw() (func(), error)
	// Columns is the current terminal width in characters.
	Columns() int
}

// Editor reads edited lines from a terminal.
type Editor struct {
	in        *bufio.Reader
	out       io.Writer
	term      termCtl
	completer Completer

	history []string

	// Per-line state.
	prompt  string
	buf     []rune
	pos     int // cursor position, in runes, 0..len(buf)
	cols    int
	histIdx int    // index into history; len(history) means "the line being typed"
	pending string // the line being typed, saved while browsing history
	yank    string // last killed text, for Ctrl-Y
}

// New returns an Editor reading from in and writing to out.  It fails with
// ErrNotTerminal when in is not a tty (a pipe, a redirected file, a test
// harness), which is the caller's cue to read lines the plain way.
func New(in *os.File, out io.Writer) (*Editor, error) {
	if !IsTerminal(in) {
		return nil, ErrNotTerminal
	}
	return newEditor(in, out, fdTerm{fd: in.Fd()}), nil
}

func newEditor(in io.Reader, out io.Writer, term termCtl) *Editor {
	return &Editor{
		in:   bufio.NewReader(in),
		out:  out,
		term: term,
	}
}

// SetCompleter installs the TAB completion callback.
func (e *Editor) SetCompleter(c Completer) { e.completer = c }

// AddHistory appends a line to the history, skipping blank lines and immediate
// repeats, and trims the history to its maximum length.
func (e *Editor) AddHistory(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		return
	}
	e.history = append(e.history, line)
	if len(e.history) > maxHistory {
		e.history = append(e.history[:0], e.history[len(e.history)-maxHistory:]...)
	}
}

// History returns a copy of the current history, oldest first.
func (e *Editor) History() []string {
	return append([]string(nil), e.history...)
}

// ReadLine prints prompt and returns the line the user accepted with Enter.
// It returns ErrInterrupted on Ctrl-C and io.EOF on Ctrl-D at an empty line.
// The returned line never contains control characters.
func (e *Editor) ReadLine(prompt string) (string, error) {
	restore, err := e.term.MakeRaw()
	if err != nil {
		return "", err
	}
	defer restore()

	e.prompt = prompt
	e.buf = e.buf[:0]
	e.pos = 0
	e.histIdx = len(e.history)
	e.pending = ""
	e.cols = e.term.Columns()

	e.write(prompt)

	for {
		r, _, err := e.in.ReadRune()
		if err != nil {
			// EOF mid-line: treat a typed-but-unaccepted line as discarded,
			// exactly like Ctrl-D on a non-empty line does nothing.
			e.write("\r\n")
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			return "", err
		}

		switch r {
		case keyEnter, keyCtrlJ:
			e.finishLine()
			return string(e.buf), nil

		case keyCtrlC:
			// Move to the end first: "^C" printed at a mid-line cursor would
			// overwrite the text the operator can still see.
			e.finishLine("^C")
			return "", ErrInterrupted

		case keyCtrlD:
			if len(e.buf) == 0 {
				e.write("\r\n")
				return "", io.EOF
			}
			e.deleteForward()

		case keyTab:
			e.complete()

		case keyBackspace, keyCtrlH:
			e.deleteBackward()

		case keyCtrlA:
			e.pos = 0
			e.refresh()
		case keyCtrlE:
			e.pos = len(e.buf)
			e.refresh()
		case keyCtrlB:
			e.moveLeft()
		case keyCtrlF:
			e.moveRight()
		case keyCtrlP:
			e.historyMove(-1)
		case keyCtrlN:
			e.historyMove(1)

		case keyCtrlK:
			e.killToEnd()
		case keyCtrlU:
			e.killToStart()
		case keyCtrlW:
			e.killWordBack()
		case keyCtrlY:
			e.insertString(e.yank)
		case keyCtrlT:
			e.transpose()

		case keyCtrlL:
			// Clear screen, home the cursor, redraw the current line.
			e.write("\x1b[H\x1b[2J")
			e.refresh()

		case keyEsc:
			e.handleEscape()

		default:
			// Anything else that is not printable (a stray control byte, an
			// invalid UTF-8 sequence) is dropped rather than inserted: it can
			// only come from a key we do not understand, and letting it into
			// the buffer is precisely the bug this package exists to fix.
			if r == utf8.RuneError || !unicode.IsPrint(r) {
				continue
			}
			e.insertRune(r)
		}
	}
}

// --- editing primitives ---

func (e *Editor) write(s string) { _, _ = io.WriteString(e.out, s) }

// finishLine parks the cursor at the end of the edited text, optionally writes
// a trailing marker such as "^C", and breaks the line. Everything printed after
// a line is done must go through here so it cannot land on top of the text.
func (e *Editor) finishLine(marker ...string) {
	e.pos = len(e.buf)
	e.refresh()
	for _, m := range marker {
		e.write(m)
	}
	e.write("\r\n")
}

func (e *Editor) insertRune(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
	e.refresh()
}

func (e *Editor) insertString(s string) {
	if s == "" {
		return
	}
	rs := []rune(s)
	e.buf = append(e.buf, rs...)
	copy(e.buf[e.pos+len(rs):], e.buf[e.pos:len(e.buf)-len(rs)])
	copy(e.buf[e.pos:], rs)
	e.pos += len(rs)
	e.refresh()
}

// setLine replaces the whole line (used by history navigation).
func (e *Editor) setLine(s string) {
	e.buf = append(e.buf[:0], []rune(s)...)
	e.pos = len(e.buf)
	e.refresh()
}

func (e *Editor) moveLeft() {
	if e.pos > 0 {
		e.pos--
		e.refresh()
	}
}

func (e *Editor) moveRight() {
	if e.pos < len(e.buf) {
		e.pos++
		e.refresh()
	}
}

func (e *Editor) deleteBackward() {
	if e.pos == 0 {
		return
	}
	e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
	e.pos--
	e.refresh()
}

func (e *Editor) deleteForward() {
	if e.pos >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	e.refresh()
}

func (e *Editor) killToEnd() {
	if e.pos >= len(e.buf) {
		return
	}
	e.yank = string(e.buf[e.pos:])
	e.buf = e.buf[:e.pos]
	e.refresh()
}

func (e *Editor) killToStart() {
	if e.pos == 0 {
		return
	}
	e.yank = string(e.buf[:e.pos])
	e.buf = append(e.buf[:0], e.buf[e.pos:]...)
	e.pos = 0
	e.refresh()
}

// wordStart returns the index of the start of the word left of the cursor,
// skipping any whitespace directly left of it.
func (e *Editor) wordStart() int {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	return i
}

// wordEnd returns the index just past the word right of the cursor.
func (e *Editor) wordEnd() int {
	i := e.pos
	for i < len(e.buf) && unicode.IsSpace(e.buf[i]) {
		i++
	}
	for i < len(e.buf) && !unicode.IsSpace(e.buf[i]) {
		i++
	}
	return i
}

func (e *Editor) killWordBack() {
	start := e.wordStart()
	if start == e.pos {
		return
	}
	e.yank = string(e.buf[start:e.pos])
	e.buf = append(e.buf[:start], e.buf[e.pos:]...)
	e.pos = start
	e.refresh()
}

func (e *Editor) killWordForward() {
	end := e.wordEnd()
	if end == e.pos {
		return
	}
	e.yank = string(e.buf[e.pos:end])
	e.buf = append(e.buf[:e.pos], e.buf[end:]...)
	e.refresh()
}

func (e *Editor) transpose() {
	if len(e.buf) < 2 || e.pos == 0 {
		return
	}
	i := e.pos
	if i >= len(e.buf) {
		i = len(e.buf) - 1
	}
	e.buf[i-1], e.buf[i] = e.buf[i], e.buf[i-1]
	if e.pos < len(e.buf) {
		e.pos++
	}
	e.refresh()
}

func (e *Editor) historyMove(delta int) {
	if len(e.history) == 0 {
		return
	}
	if e.histIdx == len(e.history) {
		e.pending = string(e.buf)
	}
	idx := e.histIdx + delta
	if idx < 0 {
		idx = 0
	}
	if idx > len(e.history) {
		idx = len(e.history)
	}
	if idx == e.histIdx {
		return
	}
	e.histIdx = idx
	if idx == len(e.history) {
		e.setLine(e.pending)
		return
	}
	e.setLine(e.history[idx])
}

// --- escape sequence handling ---

// handleEscape consumes one escape sequence.  Sequences we recognise become
// editing actions; every other sequence is read to its end and discarded, so
// an unknown key can never leave stray bytes in the buffer.
func (e *Editor) handleEscape() {
	b, err := e.in.ReadByte()
	if err != nil {
		return
	}
	switch b {
	case '[':
		e.handleCSI()
	case 'O':
		// Application cursor mode: ESC O A..D / H / F.
		c, err := e.in.ReadByte()
		if err != nil {
			return
		}
		e.cursorKey(rune(c), false)
	case 'b': // Alt-B: word left
		e.pos = e.wordStart()
		e.refresh()
	case 'f': // Alt-F: word right
		e.pos = e.wordEnd()
		e.refresh()
	case 'd': // Alt-D: kill word forward
		e.killWordForward()
	case keyBackspace, keyCtrlH: // Alt-Backspace: kill word backward
		e.killWordBack()
	}
	// Any other ESC-x is ignored.
}

// handleCSI reads the remainder of a CSI sequence (parameter and intermediate
// bytes up to the final byte in 0x40..0x7e) and dispatches it.
func (e *Editor) handleCSI() {
	var params []byte
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			return
		}
		if b >= 0x40 && b <= 0x7e {
			e.dispatchCSI(string(params), rune(b))
			return
		}
		params = append(params, b)
		// A well-formed sequence is short; bail out on anything absurd rather
		// than reading the rest of the input as parameters.
		if len(params) > 16 {
			return
		}
	}
}

func (e *Editor) dispatchCSI(params string, final rune) {
	// A modifier is reported as a second parameter: "1;5C" is Ctrl-Right,
	// "1;3C" Alt-Right.  Treat both as word-wise movement.
	modified := false
	if i := strings.IndexByte(params, ';'); i >= 0 {
		mod := params[i+1:]
		modified = mod != "" && mod != "1"
		params = params[:i]
	}

	switch final {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		e.cursorKey(final, modified)
	case '~':
		switch params {
		case "1", "7":
			e.pos = 0
			e.refresh()
		case "3":
			e.deleteForward()
		case "4", "8":
			e.pos = len(e.buf)
			e.refresh()
		}
	}
	// Everything else (colours, mouse reports, bracketed paste markers, …) is
	// deliberately ignored.
}

func (e *Editor) cursorKey(final rune, modified bool) {
	switch final {
	case 'A':
		e.historyMove(-1)
	case 'B':
		e.historyMove(1)
	case 'C':
		if modified {
			e.pos = e.wordEnd()
			e.refresh()
			return
		}
		e.moveRight()
	case 'D':
		if modified {
			e.pos = e.wordStart()
			e.refresh()
			return
		}
		e.moveLeft()
	case 'H':
		e.pos = 0
		e.refresh()
	case 'F':
		e.pos = len(e.buf)
		e.refresh()
	}
}

// --- completion ---

func (e *Editor) complete() {
	if e.completer == nil {
		return
	}
	head := string(e.buf[:e.pos])
	start, cands := e.completer(head)
	if len(cands) == 0 {
		return
	}
	if start < 0 || start > len(head) {
		return
	}
	frag := head[start:]

	// Keep only the candidates that actually extend what was typed; a
	// completer that ignores the prefix must not silently replace the word.
	matches := cands[:0:0]
	for _, c := range cands {
		if strings.HasPrefix(c, frag) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return
	}

	if len(matches) == 1 {
		e.replaceFragment(start, matches[0]+" ")
		return
	}

	if lcp := commonPrefix(matches); len(lcp) > len(frag) {
		e.replaceFragment(start, lcp)
		return
	}
	e.showCandidates(matches)
}

// replaceFragment swaps the text from byte offset start (within the text left
// of the cursor) up to the cursor for repl.
func (e *Editor) replaceFragment(start int, repl string) {
	head := string(e.buf[:e.pos])
	keep := utf8.RuneCountInString(head[:start])
	rs := []rune(repl)
	tail := append([]rune(nil), e.buf[e.pos:]...)
	e.buf = append(e.buf[:keep], rs...)
	e.buf = append(e.buf, tail...)
	e.pos = keep + len(rs)
	e.refresh()
}

// showCandidates lists the candidates in columns below the prompt and redraws
// the line, the way readline's possible-completions display does.
func (e *Editor) showCandidates(cands []string) {
	width := 0
	for _, c := range cands {
		if n := utf8.RuneCountInString(c); n > width {
			width = n
		}
	}
	width += 2
	perLine := e.cols / width
	if perLine < 1 {
		perLine = 1
	}

	var b strings.Builder
	b.WriteString("\r\n")
	for i, c := range cands {
		b.WriteString(c)
		if (i+1)%perLine == 0 || i == len(cands)-1 {
			b.WriteString("\r\n")
			continue
		}
		b.WriteString(strings.Repeat(" ", width-utf8.RuneCountInString(c)))
	}
	e.write(b.String())
	e.refresh()
}

func commonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0]
	for _, s := range items[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// --- rendering ---

// refresh redraws the visible line.  Only one physical line is ever used: when
// the text is wider than the terminal it scrolls horizontally around the
// cursor, which keeps the cursor arithmetic exact regardless of wrapping.
func (e *Editor) refresh() {
	e.cols = e.term.Columns()
	plen := utf8.RuneCountInString(e.prompt)
	if plen > e.cols-2 {
		plen = 0 // absurdly long prompt: drop it rather than lose the line
	}

	buf := e.buf
	pos := e.pos
	start := 0
	for plen+pos-start >= e.cols {
		start++
	}
	end := start
	for end < len(buf) && plen+end-start < e.cols-1 {
		end++
	}

	var b strings.Builder
	b.WriteString("\r")
	if plen > 0 {
		b.WriteString(e.prompt)
	}
	b.WriteString(string(buf[start:end]))
	b.WriteString("\x1b[0K") // erase what the previous, longer line left behind
	if n := plen + pos - start; n > 0 {
		b.WriteString("\r")
		fmt.Fprintf(&b, "\x1b[%dC", n)
	} else {
		b.WriteString("\r")
	}
	e.write(b.String())
}
