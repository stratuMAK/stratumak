// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package emccalib implements the live calibration tuning gomod.
//
// It reads the calibreg registry (populated during HAL file loading) to
// discover which HAL pins were set from INI [SECTION]KEY references, then
// exposes REST endpoints to query tunables, apply new values, and save
// changes back to the INI file(s).
package emccalib

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	emccalibapi "github.com/stratuMAK/stratumak/src/stmak/generated/gmi/emccalib"
	"github.com/stratuMAK/stratumak/src/stmak/internal/apiserver"
	"github.com/stratuMAK/stratumak/src/stmak/internal/calibreg"
	"github.com/stratuMAK/stratumak/src/stmak/internal/halcmd"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/stmak"
)

func init() {
	stmak.RegisterModule("emccalib", newEmccalib)
	apiserver.RegisterMeta(emccalibapi.EmccalibMeta)
}

// tunable is an internal representation of a discovered tunable item.
type tunable struct {
	section    string
	key        string
	pin        string
	iniValue   float64
	sourceFile string // file to write back to
	sourceLine int    // line number in that file
}

type emccalib struct {
	logger *slog.Logger
	ini    *inifile.IniFile
	mu     sync.Mutex
	// tunables holds all discovered tunables; index maps "SECTION\x00KEY" to
	// the positions in it.  index deliberately stores positions, not *tunable:
	// pointers taken into a slice that is still being appended to go stale the
	// moment append reallocates, which silently split the lookup path off from
	// the iteration path (see the aliasing note on newEmccalib).  It is a
	// *slice* of positions because one [SECTION]KEY may feed several HAL pins
	// (tandem/gantry configs setp two PID gains from one INI key); set_pin and
	// revert must fan out to all of them, not just the last one registered.
	tunables []tunable
	index    map[string][]int
}

// lookupAll returns the positions of every tunable for a section/key (empty if
// it is not tunable).  The caller must hold e.mu, and must keep holding it
// while dereferencing them — SaveIni writes iniValue through the same slice.
func (e *emccalib) lookupAll(section, key string) []int {
	return e.index[section+"\x00"+key]
}

func newEmccalib(ini *inifile.IniFile, logger *slog.Logger, name string, args []string) (stmak.Module, error) {
	e := &emccalib{
		logger: logger,
		ini:    ini,
		index:  make(map[string][]int),
	}

	// Build tunable list from calibreg (populated during HAL file loading).
	mappings := calibreg.GetAll()
	for _, m := range mappings {
		t := tunable{
			section:  m.Section,
			key:      m.Key,
			pin:      m.Pin,
			iniValue: m.IniValue,
		}
		// Find provenance — which file and line this entry came from.
		// ini is nil in an INI-less launcher (halrun mode); the tunables
		// themselves come from calibreg, so provenance is simply unavailable.
		for i := 0; ini != nil && i < len(ini.Sections); i++ {
			if ini.Sections[i].Name != m.Section {
				continue
			}
			for j := range ini.Sections[i].Entries {
				entry := &ini.Sections[i].Entries[j]
				if entry.Key == m.Key {
					t.sourceFile = entry.SourceFile
					t.sourceLine = entry.SourceLine
					break
				}
			}
			if t.sourceFile != "" {
				break
			}
		}
		e.tunables = append(e.tunables, t)
		k := m.Section + "\x00" + m.Key
		e.index[k] = append(e.index[k], len(e.tunables)-1)
	}

	logger.Info("emccalib: discovered tunables", "count", len(e.tunables))

	// Register with apiserver under the *instance* name, like every other
	// gomod.  This used to be the literal "emccalib", so `load emccalib
	// <mycalib>` published its API at the wrong address and a second instance
	// collided with the first instead of getting its own.
	reg := apiserver.DefaultRegistry()
	if err := emccalibapi.RegisterEmccalibAPI(reg, name, e); err != nil {
		return nil, fmt.Errorf("emccalib: register API: %w", err)
	}

	return e, nil
}

func (e *emccalib) Start() error { return nil }
func (e *emccalib) Stop()        {}
func (e *emccalib) Destroy()     {}

// --- EmccalibCallbacks implementation ---

func (e *emccalib) GetTunables() ([]emccalibapi.TunableSection, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Group tunables by section.
	sectionMap := make(map[string]*emccalibapi.TunableSection)
	var sectionOrder []string

	for i := range e.tunables {
		t := &e.tunables[i]
		sec, ok := sectionMap[t.section]
		if !ok {
			sec = &emccalibapi.TunableSection{Name: t.section}
			sectionMap[t.section] = sec
			sectionOrder = append(sectionOrder, t.section)
		}

		// Read current HAL pin value.
		valStr, err := halcmd.GetP(t.pin)
		var val float64
		if err == nil {
			val, _ = strconv.ParseFloat(strings.TrimSpace(valStr), 64)
		}

		sec.Items = append(sec.Items, emccalibapi.TunableItem{
			Section:  t.section,
			Key:      t.key,
			HalPin:   t.pin,
			Value:    val,
			IniValue: t.iniValue,
		})
	}

	// Return in discovery order.
	result := make([]emccalibapi.TunableSection, 0, len(sectionOrder))
	for _, name := range sectionOrder {
		result = append(result, *sectionMap[name])
	}
	return result, nil
}

func (e *emccalib) SetPin(section, key string, value float64) (bool, error) {
	// The pin names are copied out under the lock rather than used through
	// pointers: SaveIni mutates the same elements, so touching a *tunable after
	// unlocking is a data race.
	e.mu.Lock()
	var pins []string
	for _, i := range e.lookupAll(section, key) {
		pins = append(pins, e.tunables[i].pin)
	}
	e.mu.Unlock()
	if len(pins) == 0 {
		return false, apiserver.Faultf(apiserver.FaultNotFound,
			"emccalib: %s/%s not in tunable list", section, key)
	}

	// One [SECTION]KEY may feed several pins (tandem axes); a partial failure
	// still reports every pin that could not be set.
	valStr := strconv.FormatFloat(value, 'f', -1, 64)
	var errs []error
	for _, pin := range pins {
		if err := halcmd.SetP(pin, valStr); err != nil {
			errs = append(errs, fmt.Errorf("emccalib: setp %s %s: %w", pin, valStr, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	return true, nil
}

func (e *emccalib) SaveIni() (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Group tunables by source file.
	fileUpdates := make(map[string][]tunable)
	for i := range e.tunables {
		t := &e.tunables[i]
		if t.sourceFile == "" {
			continue
		}
		// Read current value from HAL.
		valStr, err := halcmd.GetP(t.pin)
		if err != nil {
			e.logger.Warn("emccalib: cannot read pin for save", "pin", t.pin, "err", err)
			continue
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(valStr), 64)
		if err != nil {
			continue
		}
		// Only save if value differs from original INI value.  The compare is
		// within the resolution of halcmd's "%.7g" formatting: an exact !=
		// rewrote every INI entry with >7 significant digits to its truncated
		// readback even though the operator never touched it.
		if !floatsClose(val, t.iniValue) {
			updated := *t
			updated.iniValue = val // new value to write
			fileUpdates[t.sourceFile] = append(fileUpdates[t.sourceFile], updated)
		}
	}

	// Write each modified file.
	for file, updates := range fileUpdates {
		if err := e.updateINIFile(file, updates); err != nil {
			return false, fmt.Errorf("emccalib: saving %s: %w", file, err)
		}
	}

	// Update in-memory ini_value to reflect the saved state.
	for i := range e.tunables {
		t := &e.tunables[i]
		valStr, err := halcmd.GetP(t.pin)
		if err == nil {
			val, err2 := strconv.ParseFloat(strings.TrimSpace(valStr), 64)
			if err2 == nil {
				t.iniValue = val
			}
		}
	}

	return true, nil
}

func (e *emccalib) Revert(section, key string) (bool, error) {
	// iniValue must be read under the lock: SaveIni rewrites it to the value
	// just persisted, and "revert" means back to what is in the INI file *now*,
	// not back to what it held at startup.
	type target struct {
		pin      string
		iniValue float64
	}
	e.mu.Lock()
	var targets []target
	for _, i := range e.lookupAll(section, key) {
		t := &e.tunables[i]
		targets = append(targets, target{pin: t.pin, iniValue: t.iniValue})
	}
	e.mu.Unlock()
	if len(targets) == 0 {
		return false, apiserver.Faultf(apiserver.FaultNotFound,
			"emccalib: %s/%s not in tunable list", section, key)
	}

	var errs []error
	for _, tg := range targets {
		valStr := strconv.FormatFloat(tg.iniValue, 'f', -1, 64)
		if err := halcmd.SetP(tg.pin, valStr); err != nil {
			errs = append(errs, fmt.Errorf("emccalib: revert setp %s %s: %w", tg.pin, valStr, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	return true, nil
}

// updateINIFile rewrites a single INI file, replacing values at specific lines.
// Creates a .bak backup before modification.
func (e *emccalib) updateINIFile(path string, updates []tunable) error {
	// The path comes from INI entry provenance, and this rewrites the file in
	// place (plus a .bak alongside it), so it goes through the shared resolver
	// in Write mode: a save must land inside the allowed roots
	// (internal/pathres).
	path, err := pathres.Resolve(path, pathres.Write)
	if err != nil {
		return err
	}

	// Build line→(key, value) map for O(1) lookup during scan.  The key is
	// carried along so the rewrite can confirm it is still editing the entry it
	// recorded: source lines are captured when the INI is parsed at startup and
	// a save can happen much later, so an INI edited on disk in the meantime
	// would otherwise have an unrelated key silently overwritten.
	type lineUpdate struct{ key, value string }
	lineUpdates := make(map[int]lineUpdate, len(updates))
	for _, u := range updates {
		lineUpdates[u.sourceLine] = lineUpdate{
			key:   u.key,
			value: strconv.FormatFloat(u.iniValue, 'f', -1, 64),
		}
	}

	// Read original file.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = f.Close()

	// Apply updates.
	for lineNum, up := range lineUpdates {
		idx := lineNum - 1 // 1-based → 0-based
		if idx < 0 || idx >= len(lines) {
			continue
		}
		if got := iniLineKey(lines[idx]); got != up.key {
			e.logger.Warn("emccalib: skipping save, INI line no longer holds this key",
				"file", path, "line", lineNum, "want", up.key, "got", got)
			continue
		}
		lines[idx] = replaceINIValue(lines[idx], up.value)
	}

	// Create backup.
	bakPath := path + ".bak"
	if err := copyFile(path, bakPath); err != nil {
		return fmt.Errorf("creating backup: %w", err)
	}

	// Write modified file.
	out, err := os.Create(path)
	if err != nil {
		return err
	}

	// bufio.Writer errors are sticky and surface at Flush; the write-file
	// Close is checked explicitly so a failed final flush/close is not lost.
	w := bufio.NewWriter(out)
	for i, line := range lines {
		_, _ = w.WriteString(line)
		if i < len(lines)-1 {
			_ = w.WriteByte('\n')
		}
	}
	// Terminate the last line.  bufio.Scanner drops the line ending, so whether
	// the original had a trailing newline is not recoverable here; always
	// writing one keeps the file POSIX-well-formed.
	_ = w.WriteByte('\n')
	if err := w.Flush(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// iniLineKey returns the key of a "KEY = VALUE" line, or "" if the line is not
// an assignment (a comment, a [SECTION] header, or blank).
func iniLineKey(line string) string {
	if s := strings.TrimSpace(line); s == "" || s[0] == '#' || s[0] == ';' || s[0] == '[' {
		return ""
	}
	eqIdx := strings.Index(line, "=")
	if eqIdx < 0 {
		return ""
	}
	return strings.TrimSpace(line[:eqIdx])
}

// replaceINIValue replaces the value portion of a "KEY = VALUE" line,
// preserving the key name, any leading whitespace/formatting, and any trailing
// inline comment (which the INI parser strips but the file still carries — a
// save that dropped it destroyed the operator's own annotations).
func replaceINIValue(line, newVal string) string {
	eqIdx := strings.Index(line, "=")
	if eqIdx < 0 {
		return line
	}
	// Preserve everything up to and including "= " (with one space after =).
	prefix := line[:eqIdx+1]
	// Check if there was a space after =.
	rest := line[eqIdx+1:]
	if len(rest) > 0 && rest[0] == ' ' {
		prefix += " "
	}
	// Re-attach any inline comment, split at exactly the boundary the INI
	// parser reads at (whitespace-preceded '#'; ';' is data, not a comment).
	// The parser splits the *trimmed* value, so trim here too — otherwise a
	// value that merely starts with '#' (the hex colour "#ff0000") reads as an
	// all-comment line.
	_, comment := inifile.SplitInlineComment(strings.TrimSpace(rest))
	return prefix + newVal + comment
}

// floatsClose reports whether two values are equal within the resolution of
// halcmd's "%.7g" pin readback (relative error <= 5e-8; margin to 1e-6).
func floatsClose(a, b float64) bool {
	d := math.Abs(a - b)
	if d <= 1e-12 {
		return true
	}
	return d <= math.Max(math.Abs(a), math.Abs(b))*1e-6
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
