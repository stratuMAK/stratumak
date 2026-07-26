// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package haljson

import (
	"os"
	"path/filepath"
	"testing"

	halparse "github.com/sittner/linuxcnc/src/gomc/internal/halparse"
)

// writeTempConfig writes content to a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

// templateData builds a HalTemplateData from a simple INI map for tests.
func templateData() *halparse.HalTemplateData {
	return halparse.NewHalTemplateData(map[string]map[string]string{
		"TRAJ": {"COORDINATES": "X Y Z"},
		"KINS": {"JOINTS": "3"},
	})
}

// pinNames flattens all pin names (leaf items) under a root, in order.
func pinNames(items []*jsonItem) []string {
	var names []string
	for _, item := range items {
		switch item.kind {
		case itemPin:
			names = append(names, item.name)
		case itemObject, itemArray:
			names = append(names, pinNames(item.children)...)
		}
	}
	return names
}

func TestParseConfigPassthrough(t *testing.T) {
	// A config with no template directives must parse unchanged, whether or not
	// a template context is supplied.
	const cfg = `<halJson>
  <halJsonRoot path="pins">
    <halJsonPin name="jx" type="float" dir="in"/>
    <halJsonPin name="jy" type="float" dir="in"/>
  </halJsonRoot>
</halJson>`
	path := writeTempConfig(t, cfg)

	for _, td := range []*halparse.HalTemplateData{nil, templateData()} {
		roots, err := parseConfig(path, td)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if len(roots) != 1 || roots[0].path != "pins" {
			t.Fatalf("unexpected roots: %+v", roots)
		}
		if got := pinNames(roots[0].items); len(got) != 2 || got[0] != "jx" || got[1] != "jy" {
			t.Fatalf("unexpected pins: %v", got)
		}
	}
}

func TestParseConfigTemplateRangeJoints(t *testing.T) {
	// range/seq over .Joints must expand into one pin per joint.
	const cfg = `<halJson>
  <halJsonRoot path="joints">
    {{- range seq 0 .Joints}}
    <halJsonPin name="{{printf "j%d" .}}" type="float" dir="in"/>
    {{- end}}
  </halJsonRoot>
</halJson>`
	path := writeTempConfig(t, cfg)

	roots, err := parseConfig(path, templateData())
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	got := pinNames(roots[0].items)
	want := []string{"j0", "j1", "j2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pin %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseConfigTemplateIniAndAxis(t *testing.T) {
	// The ini function and hasAxis helper must be available and use the
	// supplied INI context.
	const cfg = `<halJson>
  <halJsonRoot path="{{replace (ini "TRAJ" "COORDINATES" | lower) " " "_"}}">
    {{- range .Axes}}
    {{- if hasAxis .}}
    <halJsonPin name="{{lower .}}" type="float" dir="out"/>
    {{- end}}
    {{- end}}
  </halJsonRoot>
</halJson>`
	path := writeTempConfig(t, cfg)

	roots, err := parseConfig(path, templateData())
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if roots[0].path != "x_y_z" {
		t.Fatalf("unexpected path: %q", roots[0].path)
	}
	got := pinNames(roots[0].items)
	want := []string{"x", "y", "z"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pin %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseConfigTemplateError(t *testing.T) {
	// A malformed template directive must surface as an error, not silently
	// parse as XML.
	const cfg = `<halJson>
  <halJsonRoot path="pins">
    {{ this is not valid }}
  </halJsonRoot>
</halJson>`
	path := writeTempConfig(t, cfg)

	if _, err := parseConfig(path, templateData()); err == nil {
		t.Fatal("expected template error, got nil")
	}
}

// HJ-3: an array whose size exceeds maxArraySize must be rejected at parse time
// rather than allocating a huge slice / spinning up runaway hal_pin_new calls.
func TestParseConfigArraySizeBounded(t *testing.T) {
	cfg := `<halJson>
  <halJsonRoot path="pins">
    <halJsonArray name="big" size="2000000000">
      <halJsonPin name="v" type="float" dir="in"/>
    </halJsonArray>
  </halJsonRoot>
</halJson>`
	path := writeTempConfig(t, cfg)
	if _, err := parseConfig(path, templateData()); err == nil {
		t.Fatal("expected oversized-array error, got nil")
	}

	// A sane size still parses.
	ok := `<halJson>
  <halJsonRoot path="pins">
    <halJsonArray name="small" size="3">
      <halJsonPin name="v" type="float" dir="in"/>
    </halJsonArray>
  </halJsonRoot>
</halJson>`
	if _, err := parseConfig(writeTempConfig(t, ok), templateData()); err != nil {
		t.Fatalf("sane array size should parse: %v", err)
	}
}

// HJ-4: an oversized rate= arg is clamped so it cannot overflow time.Duration
// into a degenerate/negative interval.
func TestParseArgsRateClamped(t *testing.T) {
	_, rate := parseArgs([]string{"rate=999999999999999"})
	if rate != maxRateMS {
		t.Errorf("rate = %d; want clamped to %d", rate, maxRateMS)
	}
	// A normal rate passes through.
	if _, r := parseArgs([]string{"rate=50"}); r != 50 {
		t.Errorf("rate = %d; want 50", r)
	}
	// A zero/negative rate keeps the default.
	if _, r := parseArgs([]string{"rate=0"}); r != 50 {
		t.Errorf("rate = %d; want default 50", r)
	}
}

// TestParseConfigRejectsMalformedItems covers the validation error paths in
// convertItem/convertPin/convertObject/convertArray. Every one of these is a
// config authoring mistake that must fail loudly at load rather than produce a
// silently pin-less endpoint.
func TestParseConfigRejectsMalformedItems(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown element", `<halJsonThing name="x"/>`},
		{"pin without name", `<halJsonPin type="bit" dir="in"/>`},
		{"pin with unknown type", `<halJsonPin name="x" type="quad" dir="in"/>`},
		{"pin with unknown dir", `<halJsonPin name="x" type="bit" dir="sideways"/>`},
		{"param with unknown type", `<halJsonParam name="x" type="quad" dir="in"/>`},
		{"object without name", `<halJsonObject><halJsonPin name="a" type="bit" dir="in"/></halJsonObject>`},
		{"object with a bad child", `<halJsonObject name="o"><halJsonPin name="a" type="quad" dir="in"/></halJsonObject>`},
		{"array without name", `<halJsonArray size="2"><halJsonPin name="a" type="bit" dir="in"/></halJsonArray>`},
		{"array without size", `<halJsonArray name="a"><halJsonPin name="v" type="bit" dir="in"/></halJsonArray>`},
		{"array with a bad child", `<halJsonArray name="a" size="2"><halJsonPin name="v" type="quad" dir="in"/></halJsonArray>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := `<halJson><halJsonRoot path="pins">` + tc.body + `</halJsonRoot></halJson>`
			if _, err := parseConfig(writeTempConfig(t, cfg), nil); err == nil {
				t.Errorf("parseConfig(%s) = nil error; want a rejection", tc.body)
			}
		})
	}
}

// TestParseConfigDocumentErrors covers the document-level failures: a missing
// file, XML that does not parse, no roots at all, and a root with no path.
func TestParseConfigDocumentErrors(t *testing.T) {
	if _, err := parseConfig(filepath.Join(t.TempDir(), "nope.xml"), nil); err == nil {
		t.Error("missing file must be an error")
	}
	for _, tc := range []struct {
		name string
		cfg  string
	}{
		{"malformed XML", `<halJson><halJsonRoot path="p"></halJson>`},
		{"no roots", `<halJson></halJson>`},
		{"root without path", `<halJson><halJsonRoot><halJsonPin name="a" type="bit" dir="in"/></halJsonRoot></halJson>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseConfig(writeTempConfig(t, tc.cfg), nil); err == nil {
				t.Errorf("parseConfig(%s) = nil error; want a rejection", tc.cfg)
			}
		})
	}
}

// TestParseHalTypeAndDirAccepted pins the accepted spellings (including the
// case-insensitivity both parsers rely on) and the type/direction each maps to.
func TestParseHalTypeAndDirAccepted(t *testing.T) {
	for s, want := range map[string]pinType{
		"bit": pinTypeBit, "BIT": pinTypeBit,
		"float": pinTypeFloat, "Float": pinTypeFloat,
		"s32": pinTypeS32, "S32": pinTypeS32,
		"u32": pinTypeU32, "U32": pinTypeU32,
	} {
		got, err := parseHalType(s)
		if err != nil || got != want {
			t.Errorf("parseHalType(%q) = %v, %v; want %v, nil", s, got, err, want)
		}
	}
	for s, want := range map[string]pinDir{
		"in": pinDirIn, "IN": pinDirIn,
		"out": pinDirOut, "Out": pinDirOut,
		"io": pinDirIO, "IO": pinDirIO,
	} {
		got, err := parseHalDir(s)
		if err != nil || got != want {
			t.Errorf("parseHalDir(%q) = %v, %v; want %v, nil", s, got, err, want)
		}
	}
	if _, err := parseHalType(""); err == nil {
		t.Error("empty type must be rejected")
	}
	if _, err := parseHalDir(""); err == nil {
		t.Error("empty dir must be rejected")
	}
}

// TestParseArgsConfig covers the remaining parseArgs branches: the config path,
// a non-numeric rate falling back to the default, and args with no "=" being
// skipped rather than mis-parsed.
func TestParseArgsConfig(t *testing.T) {
	cfg, rate := parseArgs([]string{"noequalssign", "config=panel.xml", "rate=notanumber", "other=1"})
	if cfg != "panel.xml" {
		t.Errorf("config = %q; want panel.xml", cfg)
	}
	if rate != 50 {
		t.Errorf("rate = %d; want the 50 default for a non-numeric value", rate)
	}
	if c, r := parseArgs(nil); c != "" || r != 50 {
		t.Errorf("parseArgs(nil) = %q, %d; want \"\", 50", c, r)
	}
}
