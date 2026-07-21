// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package inifile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// substitutePattern matches [SECTION]KEY references used in HAL files.
var substitutePattern = regexp.MustCompile(`\[([^\]]+)\]([A-Za-z0-9_-]+)`)

// maxExtendLines caps the number of backslash line-continuations for a single
// logical line, matching the LinuxCNC C parser's MAX_EXTEND_LINES.
const maxExtendLines = 20

// Parse reads and parses an INI file, recursively handling #INCLUDE directives.
// Relative paths in #INCLUDE are resolved relative to the directory of the
// file containing the directive.  Environment variables (e.g. $HOME) in
// #INCLUDE paths are expanded before resolution.
func Parse(filename string) (*IniFile, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("inifile: resolving path %q: %w", filename, err)
	}
	ini := &IniFile{sourceFile: abs}
	if err := ini.parseFile(abs, map[string]bool{}); err != nil {
		return nil, err
	}
	return ini, nil
}

// parseFile parses a single file and appends its content into ini.
// visited tracks already-included files to detect simple import cycles.
func (ini *IniFile) parseFile(filename string, visited map[string]bool) error {
	if visited[filename] {
		return fmt.Errorf("inifile: circular #INCLUDE detected: %s", filename)
	}
	visited[filename] = true

	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("inifile: opening %q: %w", filename, err)
	}
	defer func() { _ = f.Close() }()

	dir := filepath.Dir(filename)
	var currentSection *Section

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		startLine := lineNum
		raw := scanner.Text()

		// Honour a trailing backslash (\) as a line-continuation escape,
		// matching the LinuxCNC C parser (libnml/inifile): when a physical
		// line's last character is a single '\', it is joined with the
		// following line(s) — up to maxExtendLines — with the backslash
		// removed and no separator inserted, so a value split across lines
		// such as
		//	APP = sim_pin \
		//	      axis.x.jog-counts
		// is read as one value. The check is made on the untrimmed line so a
		// '\' followed by trailing whitespace is not treated as continuation
		// (mirroring the C parser, which tests the last byte before newline).
		if strings.HasSuffix(raw, `\`) {
			var b strings.Builder
			b.WriteString(raw[:len(raw)-1])
			extend := 0
			for scanner.Scan() {
				lineNum++
				extend++
				if extend > maxExtendLines {
					return fmt.Errorf("inifile: %s:%d: too many backslash line continuations (limit %d)", filename, startLine, maxExtendLines)
				}
				next := scanner.Text()
				if strings.HasSuffix(next, `\`) {
					b.WriteString(next[:len(next)-1])
					continue
				}
				b.WriteString(next)
				break
			}
			raw = b.String()
		}

		line := strings.TrimSpace(raw)

		// Empty line – skip.
		if line == "" {
			continue
		}

		// #INCLUDE directive (must come before the generic # comment check).
		if strings.HasPrefix(line, "#INCLUDE") {
			rest := strings.TrimSpace(line[len("#INCLUDE"):])
			if rest == "" {
				return fmt.Errorf("inifile: %s:%d: empty #INCLUDE path", filename, startLine)
			}
			// Expand environment variables in the path.
			rest = os.ExpandEnv(rest)
			if !filepath.IsAbs(rest) {
				rest = filepath.Join(dir, rest)
			}
			abs, err := filepath.Abs(rest)
			if err != nil {
				return fmt.Errorf("inifile: %s:%d: resolving #INCLUDE path %q: %w", filename, startLine, rest, err)
			}
			// Pass a copy of visited so sibling includes don't interfere.
			visited2 := make(map[string]bool, len(visited))
			for k, v := range visited {
				visited2[k] = v
			}
			if err := ini.parseFile(abs, visited2); err != nil {
				return err
			}
			// Restore current section pointer – parseFile may have appended
			// new sections; our current section still lives at its original index.
			if currentSection != nil {
				// Re-point to the last section that matches our current name
				// (section may have been continued by the included file – keep
				// appending to the latest section with that name to stay correct).
				for i := len(ini.Sections) - 1; i >= 0; i-- {
					if ini.Sections[i].Name == currentSection.Name {
						currentSection = &ini.Sections[i]
						break
					}
				}
			}
			continue
		}

		// Generic comment lines (# not followed by INCLUDE, and ; lines).
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Section header: [NAME]
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				return fmt.Errorf("inifile: %s:%d: malformed section header %q", filename, startLine, line)
			}
			name := line[1:end]
			ini.Sections = append(ini.Sections, Section{Name: name})
			currentSection = &ini.Sections[len(ini.Sections)-1]
			continue
		}

		// Key-value pair: KEY = VALUE
		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			// Not a comment, not a section, not a key-value – ignore unknown lines.
			continue
		}
		key := strings.TrimSpace(line[:eqIdx])
		rawVal := strings.TrimSpace(line[eqIdx+1:])
		value := stripInlineComment(rawVal)

		if currentSection == nil {
			// Key before any section header – create an anonymous section.
			ini.Sections = append(ini.Sections, Section{Name: ""})
			currentSection = &ini.Sections[len(ini.Sections)-1]
		}
		currentSection.Entries = append(currentSection.Entries, Entry{Key: key, Value: value, SourceFile: filename, SourceLine: startLine})
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("inifile: reading %q: %w", filename, err)
	}
	return nil
}

// stripInlineComment removes a trailing whitespace-preceded '#' inline comment
// from a value string.
//
// The LinuxCNC C parser (libnml/inifile) does NOT strip inline comments at all:
// it recognises a comment only when '#' or ';' is the FIRST non-whitespace
// character of a line, and a value is everything after '=' (trailing whitespace
// trimmed). Numeric lookups nonetheless tolerate a trailing "# comment" because
// the C code converts with strtod, which stops at the first non-numeric byte.
// gomc reproduces exactly that tolerance — and no more — by truncating a value
// at a whitespace-preceded '#' only:
//
//   - '#' is honoured only when preceded by whitespace (" #" or "\t#"), so
//     values that embed '#' as data (e.g. the hex colour "#ff0000", or a
//     leading '#') are preserved.
//   - ';' is NOT treated as an inline comment. ';' is legitimate data in string
//     values — e.g. an MDI_COMMAND such as "G0 Z25;X0 Y0;Z0" chains G-code
//     moves — and the C parser never strips it. (An earlier version stripped at
//     the first ';', silently truncating every such MDI_COMMAND to "G0 Z25".)
//     No shipped config uses ';' as an inline comment; ';'-separated MDI
//     commands are common, so this matches both the C parser and real configs.
func stripInlineComment(s string) string {
	minIdx := len(s)
	for _, prefix := range []string{" #", "\t#"} {
		if idx := strings.Index(s, prefix); idx >= 0 && idx < minIdx {
			minIdx = idx
		}
	}
	if minIdx < len(s) {
		s = strings.TrimRight(s[:minIdx], " \t")
	}
	return s
}

// --------------------------------------------------------------------------
// Lookup helpers
// --------------------------------------------------------------------------

// Get returns the first value for the given section and key, or an empty
// string if not found.
//
// When a namespace is set, [namespace:section] is checked first, then [section].
func (ini *IniFile) Get(section, key string) string {
	if ini.namespace != "" {
		if v := ini.getRaw(ini.namespace+":"+section, key); v != "" {
			return v
		}
	}
	return ini.getRaw(section, key)
}

// getRaw looks up the first value without namespace resolution.
func (ini *IniFile) getRaw(section, key string) string {
	for i := range ini.Sections {
		if ini.Sections[i].Name != section {
			continue
		}
		for j := range ini.Sections[i].Entries {
			if ini.Sections[i].Entries[j].Key == key {
				return ini.Sections[i].Entries[j].Value
			}
		}
	}
	return ""
}

// GetAll returns all values for the given section and key, in the order they
// appear in the file.  Returns nil if the key is not present.
//
// When a namespace is set, values from [namespace:section] are returned first,
// followed by values from [section].
func (ini *IniFile) GetAll(section, key string) []string {
	var result []string
	if ini.namespace != "" {
		result = ini.getAllRaw(ini.namespace+":"+section, key)
	}
	result = append(result, ini.getAllRaw(section, key)...)
	if len(result) == 0 {
		return nil
	}
	return result
}

// getAllRaw looks up all values without namespace resolution.
func (ini *IniFile) getAllRaw(section, key string) []string {
	var result []string
	for i := range ini.Sections {
		if ini.Sections[i].Name != section {
			continue
		}
		for j := range ini.Sections[i].Entries {
			if ini.Sections[i].Entries[j].Key == key {
				result = append(result, ini.Sections[i].Entries[j].Value)
			}
		}
	}
	return result
}

// GetN returns the n-th occurrence of key in section (1-based), matching the
// behaviour of `inivar -num N`.  Returns an empty string if there is no n-th
// occurrence.
//
// When a namespace is set, occurrences from [namespace:section] are counted
// first, then [section].
func (ini *IniFile) GetN(section, key string, n int) string {
	count := 0
	if ini.namespace != "" {
		nsSection := ini.namespace + ":" + section
		for i := range ini.Sections {
			if ini.Sections[i].Name != nsSection {
				continue
			}
			for j := range ini.Sections[i].Entries {
				if ini.Sections[i].Entries[j].Key == key {
					count++
					if count == n {
						return ini.Sections[i].Entries[j].Value
					}
				}
			}
		}
	}
	for i := range ini.Sections {
		if ini.Sections[i].Name != section {
			continue
		}
		for j := range ini.Sections[i].Entries {
			if ini.Sections[i].Entries[j].Key == key {
				count++
				if count == n {
					return ini.Sections[i].Entries[j].Value
				}
			}
		}
	}
	return ""
}

// GetWithFallback tries each (section, key) pair in order and returns the
// first match, along with a boolean indicating whether any match was found.
// Each element of pairs must be a two-element slice [section, key].
// This mirrors the GetFromIniEx behaviour used in the bash script.
// Namespace-aware: each pair is looked up via Get() which checks namespaced
// sections first.
func (ini *IniFile) GetWithFallback(pairs [][2]string) (string, bool) {
	for _, p := range pairs {
		if v := ini.Get(p[0], p[1]); v != "" {
			return v, true
		}
	}
	return "", false
}

// Set updates the first occurrence of key in the named section to the given
// value.  If the key does not exist in the section, a new entry is appended.
// If the section itself does not exist, it is created with the single entry.
// Returns true if an existing entry was updated, false if a new entry was
// added.
//
// When the same section name appears more than once (e.g. via #INCLUDE), only
// entries in the first matching section are considered.
func (ini *IniFile) Set(section, key, value string) bool {
	for i := range ini.Sections {
		if ini.Sections[i].Name != section {
			continue
		}
		for j := range ini.Sections[i].Entries {
			if ini.Sections[i].Entries[j].Key == key {
				ini.Sections[i].Entries[j].Value = value
				return true
			}
		}
		// Section exists but key not found — append to first matching section.
		ini.Sections[i].Entries = append(ini.Sections[i].Entries, Entry{Key: key, Value: value})
		return false
	}
	// Section does not exist — create it with the single entry.
	ini.Sections = append(ini.Sections, Section{
		Name:    section,
		Entries: []Entry{{Key: key, Value: value}},
	})
	return false
}

// Substitute replaces [SECTION]KEY patterns in input with the corresponding
// values from the INI file.  This is used when processing HAL files that
// reference INI variables, e.g.:
//
//	loadrt motmod servo_period_nsec=[EMCMOT]SERVO_PERIOD
func (ini *IniFile) Substitute(input string) string {
	return substitutePattern.ReplaceAllStringFunc(input, func(match string) string {
		parts := substitutePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		val := ini.Get(parts[1], parts[2])
		if val == "" {
			return match
		}
		return val
	})
}

// ParseString parses INI content from a string.  This is a convenience
// function primarily intended for tests.  #INCLUDE directives are not
// supported when parsing from a string.
func ParseString(content string) (*IniFile, error) {
	f, err := os.CreateTemp("", "ini-*.ini")
	if err != nil {
		return nil, fmt.Errorf("inifile: creating temp file: %w", err)
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("inifile: writing temp file: %w", err)
	}
	// Close the write file before reading it back, so an unflushed-write error
	// surfaces rather than being masked as a parse failure.
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("inifile: closing temp file: %w", err)
	}
	return Parse(name)
}
