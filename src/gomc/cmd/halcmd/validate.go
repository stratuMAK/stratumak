// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// halNameMaxLen mirrors HAL_NAME_LEN in hal.h: hal_lib rejects longer names
// with a bare -EINVAL ("malformed name"), which tells the operator nothing.
const halNameMaxLen = 127

// checkInputLine rejects a command line that contains characters no HAL command
// can legitimately carry. It exists because such bytes used to travel all the
// way into a value and surface as a baffling error — a cursor key at a prompt
// without line editing turned "1000000" into "1\x1b[A000000", reported as
// `strconv.ParseInt: parsing "1ß000000"` (issue #265).
//
// Tabs are allowed: parseCommandLine treats them as argument separators.
func checkInputLine(line string) error {
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == utf8.RuneError && size == 1 {
			return fmt.Errorf("invalid (non-UTF-8) byte 0x%02x at position %d", line[i], i+1)
		}
		if r == '\t' {
			i += size
			continue
		}
		if unicode.IsControl(r) {
			if r == 0x1b {
				return fmt.Errorf("input contains an escape sequence at position %d "+
					"(a cursor or function key was recorded literally; "+
					"the terminal this input came from has no line editing)", i+1)
			}
			return fmt.Errorf("input contains a control character 0x%02x at position %d", r, i+1)
		}
		i += size
	}
	return nil
}

// commandPart returns line up to an unquoted '#', using the same quoting rule
// as parseCommandLine. The comment is never parsed into a command, so input
// validation must not reject bytes there: legacy HAL files carry Latin-1
// comments ("# Größe" saved as 0xF6), and refusing the whole line for a byte
// the parser was going to discard would break files that ran fine before.
func commandPart(line string) string {
	inQuote := false
	quoteChar := rune(0)
	for i, r := range line {
		switch {
		case r == '"' || r == '\'':
			if !inQuote {
				inQuote = true
				quoteChar = r
			} else if r == quoteChar {
				inQuote = false
			}
		case r == '#' && !inQuote:
			return line[:i]
		}
	}
	return line
}

// checkHALName validates a name before it is sent to the server. kind names the
// thing being created for the message ("thread", "signal", …).
func checkHALName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("empty %s name", kind)
	}
	if len(name) > halNameMaxLen {
		return fmt.Errorf("%s name %q is too long (%d characters, limit is %d)",
			kind, name, len(name), halNameMaxLen)
	}
	for _, r := range name {
		if r == utf8.RuneError || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid character %q in %s name %q", r, kind, name)
		}
	}
	return nil
}

// parseIntArg parses a decimal integer argument, reporting the offending text
// in the operator's terms rather than leaking strconv's internals. what names
// the argument ("period", "cpu", …); unit, when non-empty, is appended as a
// hint (e.g. "nanoseconds").
func parseIntArg(what, s string, bitSize int, unit string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, bitSize)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("%s %q is out of range", what, s)
	}
	msg := fmt.Sprintf("invalid %s %q: expected a whole number", what, s)
	if unit != "" {
		msg += " of " + unit
	}
	return 0, fmt.Errorf("%s", msg)
}
