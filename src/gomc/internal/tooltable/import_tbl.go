// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package tooltable

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/persist"
	"github.com/sittner/linuxcnc/src/gomc/generated/gmi/tooltable"
)

// importTbl parses a legacy LinuxCNC .tbl file and stores tools via persist API.
func (m *module) importTbl(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var entries []persist.Entry
	lineNo := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == ';' {
			continue
		}
		t, err := parseTblLine(line)
		if err != nil {
			// The C parser also skips a bad line rather than aborting the load.
			// It is logged, though: this is a one-shot migration of the
			// operator's tool data, and a silently dropped tool is a tool that
			// is simply gone the next time it is called up.
			m.logger.Warn("tooltable: skipping unparsable .tbl line",
				"path", path, "line", lineNo, "text", line, "err", err)
			continue
		}
		tool := tooltable.ToolEntry{
			Toolno:      t.toolno,
			Pocketno:    t.pocketno,
			XOffset:     t.x,
			YOffset:     t.y,
			ZOffset:     t.z,
			AOffset:     t.a,
			BOffset:     t.b,
			COffset:     t.c,
			UOffset:     t.u,
			VOffset:     t.v,
			WOffset:     t.w,
			Diameter:    t.diameter,
			Frontangle:  t.frontangle,
			Backangle:   t.backangle,
			Orientation: t.orientation,
			Comment:     t.comment,
		}
		data, err := json.Marshal(tool)
		if err != nil {
			return fmt.Errorf("marshal tool %d: %w", t.toolno, err)
		}
		entries = append(entries, persist.Entry{
			Key:   strconv.FormatInt(int64(t.toolno), 10),
			Value: string(data),
		})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(entries) > 0 {
		if _, err := m.db.SetEntries(m.dbHandle, entries); err != nil {
			return err
		}
	}
	return nil
}

type tblEntry struct {
	toolno, pocketno, orientation   int32
	x, y, z, a, b, c, u, v, w       float64
	diameter, frontangle, backangle float64
	comment                         string
}

// parseTblLine parses a line in LinuxCNC .tbl format:
// T<n> P<n> X<f> Y<f> Z<f> A<f> B<f> C<f> U<f> V<f> W<f> D<f> I<f> J<f> Q<n> ;<comment>
func parseTblLine(line string) (tblEntry, error) {
	var t tblEntry
	seenToolno := false

	// Extract comment (everything after ;)
	if idx := strings.Index(line, ";"); idx >= 0 {
		t.comment = strings.TrimSpace(line[idx+1:])
		line = line[:idx]
	}

	fields := strings.Fields(line)
	if len(fields) == 0 {
		return t, fmt.Errorf("empty line")
	}

	// Field keys are matched case-insensitively and EVERY numeric conversion is
	// checked, both to match the C parser this replaces (tooldata.cc /
	// sai_tooltable.cc: `switch (toupper(token[0]))`, and `if (!valid) return
	// -1` after each sscanf). Dropping the error was not a shortcut without
	// consequence: an unparsable "Z abc" silently became a Z offset of 0, and a
	// zeroed tool-length offset is a tool driven into the work. A malformed
	// field now rejects the whole line, exactly as the C does.
	for _, field := range fields {
		if len(field) < 2 {
			// A bare key with no value. The C hits sscanf on an empty string,
			// which returns 0 and fails the line.
			return t, fmt.Errorf("field %q has no value", field)
		}
		key := byte(unicode.ToUpper(rune(field[0])))
		val := field[1:]

		var fp *float64
		switch key {
		case 'T':
			n, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return t, fmt.Errorf("tool number %q: %w", val, err)
			}
			t.toolno = int32(n)
			seenToolno = true
			continue
		case 'P':
			n, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return t, fmt.Errorf("pocket number %q: %w", val, err)
			}
			t.pocketno = int32(n)
			continue
		case 'Q':
			n, err := strconv.ParseInt(val, 10, 32)
			if err != nil {
				return t, fmt.Errorf("orientation %q: %w", val, err)
			}
			t.orientation = int32(n)
			continue
		case 'X':
			fp = &t.x
		case 'Y':
			fp = &t.y
		case 'Z':
			fp = &t.z
		case 'A':
			fp = &t.a
		case 'B':
			fp = &t.b
		case 'C':
			fp = &t.c
		case 'U':
			fp = &t.u
		case 'V':
			fp = &t.v
		case 'W':
			fp = &t.w
		case 'D':
			fp = &t.diameter
		case 'I':
			fp = &t.frontangle
		case 'J':
			fp = &t.backangle
		default:
			// Unknown keys are ignored, as in the C parser's `default: break`.
			continue
		}

		v, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return t, fmt.Errorf("field %q: %w", field, err)
		}
		*fp = v
	}

	if !seenToolno {
		// "T0 Pn" is a legitimate entry (the random-toolchanger empty-pocket
		// marker) — only reject lines with no T field at all.
		return t, fmt.Errorf("no tool number")
	}
	return t, nil
}
