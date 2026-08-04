// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests for GetParameterFile and for the parts of Query the original suite did
// not reach (the not-loaded guard and namespaced lookups).
//
// GetParameterFile is the one method in this package that touches the disk: it
// takes a path out of the INI and serves the file's *contents* over REST. The
// INI is operator-supplied config, so the containment check is the only thing
// standing between "[RS274NGC]PARAMETER_FILE = ../../etc/shadow" and that file
// being returned to any REST caller — which is why it gets more attention here
// than the happy path does.
package inirest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/generated/gmi/ini"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// paramFileImpl builds an impl over the given INI text, with pathres rooted at
// a fresh temp dir, and writes varFiles into that root. It returns the impl and
// the root.
func paramFileImpl(t *testing.T, iniText string, varFiles map[string]string) (*iniImpl, string) {
	t.Helper()
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	for name, content := range varFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := inifile.ParseString(iniText)
	if err != nil {
		t.Fatal(err)
	}
	return &iniImpl{ini: parsed}, dir
}

// --- The not-loaded guard ---

// TestNotLoaded pins both methods' behaviour when the launcher runs without an
// INI (halrun mode). pkg/inifile's methods dereference the receiver
// immediately, so without these guards a REST call would kill the controller
// rather than return an error — the untrusted-wire lens applied to a nil.
func TestNotLoaded(t *testing.T) {
	impl := &iniImpl{ini: nil}

	if _, err := impl.Query([]ini.IniQueryItem{{Section: "EMC", Key: "MACHINE"}}); err == nil {
		t.Error("Query with no INI loaded returned no error")
	}
	if _, err := impl.GetParameterFile(sp("")); err == nil {
		t.Error("GetParameterFile with no INI loaded returned no error")
	}
}

// --- GetParameterFile ---

func TestGetParameterFile(t *testing.T) {
	const varContent = "5161\t0.000000\n5162\t0.000000\n"
	impl, _ := paramFileImpl(t,
		"[RS274NGC]\nPARAMETER_FILE = sim.var\n",
		map[string]string{"sim.var": varContent},
	)

	got, err := impl.GetParameterFile(sp(""))
	if err != nil {
		t.Fatalf("GetParameterFile: %v", err)
	}
	if got != varContent {
		t.Errorf("GetParameterFile = %q, want %q", got, varContent)
	}
}

// TestGetParameterFileNamespace covers the multi-instance path: a namespaced
// section wins over the global one, so two machines in one process each serve
// their own var file rather than both serving the first one's.
func TestGetParameterFileNamespace(t *testing.T) {
	impl, _ := paramFileImpl(t,
		"[RS274NGC]\nPARAMETER_FILE = global.var\n\n[mc2:RS274NGC]\nPARAMETER_FILE = mc2.var\n",
		map[string]string{"global.var": "global", "mc2.var": "mc2"},
	)

	if got, err := impl.GetParameterFile(sp("")); err != nil || got != "global" {
		t.Errorf("no namespace = %q, %v; want \"global\"", got, err)
	}
	if got, err := impl.GetParameterFile(sp("mc2")); err != nil || got != "mc2" {
		t.Errorf("namespace mc2 = %q, %v; want \"mc2\"", got, err)
	}
	// A namespace with no override of its own falls back to the global section
	// rather than failing — that is what makes partial per-instance INIs work.
	if got, err := impl.GetParameterFile(sp("mc3")); err != nil || got != "global" {
		t.Errorf("namespace mc3 = %q, %v; want the global fallback \"global\"", got, err)
	}
}

// TestGetParameterFileUnset: no PARAMETER_FILE is a configuration error, and it
// must be reported as one. Returning "" with a nil error would look to a caller
// exactly like a var file that exists and is empty.
func TestGetParameterFileUnset(t *testing.T) {
	impl, _ := paramFileImpl(t, "[EMC]\nMACHINE = Test\n", nil)

	got, err := impl.GetParameterFile(sp(""))
	if err == nil {
		t.Fatalf("GetParameterFile with no PARAMETER_FILE returned %q and no error", got)
	}
	if got != "" {
		t.Errorf("GetParameterFile returned %q alongside its error, want \"\"", got)
	}
	if !strings.Contains(err.Error(), "PARAMETER_FILE") {
		t.Errorf("error %q does not name the missing key", err)
	}
}

// TestGetParameterFileMissing: the key is set but the file is not there. This
// is distinct from "not configured" and must not be silently empty either — a
// GUI that got "" would show an empty parameter table instead of an error.
func TestGetParameterFileMissing(t *testing.T) {
	impl, _ := paramFileImpl(t, "[RS274NGC]\nPARAMETER_FILE = nosuch.var\n", nil)

	if got, err := impl.GetParameterFile(sp("")); err == nil {
		t.Errorf("GetParameterFile of a missing file returned %q and no error", got)
	}
}

// TestGetParameterFileUnreadable covers the gap between "resolves" and "can be
// read": the resolver only stats the path, so a var file whose permissions deny
// the controller must surface as an error rather than as empty contents. An
// empty parameter table is a plausible-looking answer, which is what makes
// swallowing this failure worse than reporting it.
func TestGetParameterFileUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}
	impl, dir := paramFileImpl(t,
		"[RS274NGC]\nPARAMETER_FILE = sim.var\n",
		map[string]string{"sim.var": "5161\t0.0\n"},
	)
	path := filepath.Join(dir, "sim.var")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	got, err := impl.GetParameterFile(sp(""))
	if err == nil {
		t.Fatalf("an unreadable var file returned %q and no error", got)
	}
	if got != "" {
		t.Errorf("returned %q alongside the error, want \"\"", got)
	}
}

// TestGetParameterFileContainment is the reason this method resolves at all.
// The path is operator-supplied INI content served over REST, so a value that
// points outside the configuration root must be refused rather than read.
// Traversal and absolute escapes are both tried, and the target really exists —
// a test that pointed at a nonexistent path would pass on "not found" and prove
// nothing about containment.
func TestGetParameterFileContainment(t *testing.T) {
	// Lay the two directories out as siblings under a common parent, so that
	// "../outside/secret.var" genuinely reaches the target. Two independent
	// t.TempDir() calls are *not* siblings in a predictable way, and a
	// traversal that misses fails with "not found" — which passes an
	// any-error assertion while proving nothing about containment.
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	outsideDir := filepath.Join(root, "outside")
	for _, d := range []string{configDir, outsideDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "not for REST callers"
	secretPath := filepath.Join(outsideDir, "secret.var")
	if err := os.WriteFile(secretPath, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"absolute path outside the root", secretPath},
		{"traversal out of the root", "../outside/secret.var"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pathres.SetDefaultForTest(t, configDir)
			parsed, err := inifile.ParseString("[RS274NGC]\nPARAMETER_FILE = " + tc.value + "\n")
			if err != nil {
				t.Fatal(err)
			}
			impl := &iniImpl{ini: parsed}

			got, err := impl.GetParameterFile(sp(""))
			if err == nil {
				t.Fatalf("an out-of-root PARAMETER_FILE was served: %q", got)
			}
			if strings.Contains(got, secret) {
				t.Fatalf("the out-of-root file's contents leaked: %q", got)
			}
			// The refusal must be the containment check, not a "not found":
			// the file really is there, and a test that accepted any error
			// would still pass if containment were removed entirely.
			if !strings.Contains(err.Error(), "outside the allowed directories") {
				t.Errorf("refused with %q, want the containment error — "+
					"this test only proves containment if that is the reason", err)
			}
		})
	}
}

// TestGetParameterFileInRootTraversal: a traversal that lands back *inside* the
// root is legitimate and must still work. Pinning this keeps the containment
// fix honest — refusing every path containing ".." would pass the test above
// while breaking ordinary configs that reach a sibling directory.
func TestGetParameterFileInRootTraversal(t *testing.T) {
	dir := t.TempDir()
	pathres.SetDefaultForTest(t, dir)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sim.var"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := inifile.ParseString("[RS274NGC]\nPARAMETER_FILE = sub/../sim.var\n")
	if err != nil {
		t.Fatal(err)
	}
	impl := &iniImpl{ini: parsed}

	if got, err := impl.GetParameterFile(sp("")); err != nil || got != "ok" {
		t.Errorf("a traversal staying inside the root = %q, %v; want \"ok\"", got, err)
	}
}

// --- Query: the branches the original suite did not reach ---

// TestQueryNamespace covers the per-instance lookup: [ns:SECTION] wins over
// [SECTION], and a namespace without its own entry falls back to the global.
func TestQueryNamespace(t *testing.T) {
	parsed, err := inifile.ParseString(`
[EMC]
MACHINE = global

[mc2:EMC]
MACHINE = second

[DISPLAY]
GEOMETRY = XYZ
`)
	if err != nil {
		t.Fatal(err)
	}
	impl := &iniImpl{ini: parsed}

	results, err := impl.Query([]ini.IniQueryItem{
		{Section: "EMC", Key: "MACHINE"},
		{Section: "EMC", Key: "MACHINE", Namespace: sp("mc2")},
		{Section: "EMC", Key: "MACHINE", Namespace: sp("mc3")},
		{Section: "DISPLAY", Key: "GEOMETRY", Namespace: sp("mc2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"global", "second", "global", "XYZ"}
	for i, w := range want {
		if derefStr(results[i].Value) != w {
			t.Errorf("result[%d] = %q, want %q", i, derefStr(results[i].Value), w)
		}
	}
}

// TestQueryNamespaceAll covers the same fallback on the findall path.
func TestQueryNamespaceAll(t *testing.T) {
	parsed, err := inifile.ParseString(`
[FILTER]
PROGRAM_EXTENSION = .nc

[mc2:FILTER]
PROGRAM_EXTENSION = .ngc
PROGRAM_EXTENSION = .py
`)
	if err != nil {
		t.Fatal(err)
	}
	impl := &iniImpl{ini: parsed}

	results, err := impl.Query([]ini.IniQueryItem{
		{Section: "FILTER", Key: "PROGRAM_EXTENSION", All: boolPtr(true), Namespace: sp("mc2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results[0].Values) == 0 {
		t.Fatal("namespaced findall returned nothing")
	}
	if results[0].Values[0] != ".ngc" {
		t.Errorf("namespaced findall = %v, want the [mc2:FILTER] entries first", results[0].Values)
	}
}

// TestQueryEmptyRequest: a bulk query with no items is a valid request for
// nothing, not an error. The generated handler hands the slice straight
// through, so an empty POST body must not become a nil-deref or a 500.
func TestQueryEmptyRequest(t *testing.T) {
	impl := setupTestINI(t)

	results, err := impl.Query(nil)
	if err != nil {
		t.Fatalf("Query(nil): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Query(nil) returned %d results, want 0", len(results))
	}
}

// TestQueryAbsentVsEmpty pins the distinction the IDL always promised and the
// code always tried to make: `value: string?` is "null if not found", so a key
// that is absent reports null while a key present with an empty value reports
// "".
//
// This could not work until `string?` became a real *string on the provider
// side. The server emitter mapped it to a plain `string`, so Query's two
// branches — IniQueryResult{} for absent, {Value: v} for present — produced the
// same value and marshalled to the same bytes, and the keyExists call that
// chose between them could not affect any caller. An INI may legitimately carry
// `LATHE =`, which is why the difference is worth having here.
func TestQueryAbsentVsEmpty(t *testing.T) {
	parsed, err := inifile.ParseString("[DISPLAY]\nLATHE =\nGEOMETRY = XYZ\n")
	if err != nil {
		t.Fatal(err)
	}
	impl := &iniImpl{ini: parsed}

	results, err := impl.Query([]ini.IniQueryItem{
		{Section: "DISPLAY", Key: "LATHE"},    // present, empty value
		{Section: "DISPLAY", Key: "NOSUCH"},   // absent key
		{Section: "NOSUCH", Key: "WHATEVER"},  // absent section
		{Section: "DISPLAY", Key: "GEOMETRY"}, // present, non-empty
	})
	if err != nil {
		t.Fatal(err)
	}

	if results[0].Value == nil {
		t.Error("a key present with an empty value reported null; want a non-nil \"\"")
	} else if *results[0].Value != "" {
		t.Errorf("present-but-empty reported %q, want \"\"", *results[0].Value)
	}
	if results[1].Value != nil {
		t.Errorf("an absent key reported %q, want null", *results[1].Value)
	}
	if results[2].Value != nil {
		t.Errorf("a key in an absent section reported %q, want null", *results[2].Value)
	}
	if results[3].Value == nil || *results[3].Value != "XYZ" {
		t.Errorf("present value = %v, want \"XYZ\"", results[3].Value)
	}

	// The distinction has to survive to the wire, which is the only thing a
	// REST caller sees. `omitempty` on a *string omits only nil.
	enc := func(r ini.IniQueryResult) string {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal %+v: %v", r, err)
		}
		return string(b)
	}
	if got, want := enc(results[0]), `{"value":"","values":null}`; got != want {
		t.Errorf("present-but-empty encodes as %s, want %s", got, want)
	}
	if got, want := enc(results[1]), `{"values":null}`; got != want {
		t.Errorf("absent encodes as %s, want %s", got, want)
	}

	// keyExists is what makes the choice; it stays directly covered.
	if !impl.keyExists("DISPLAY", "LATHE") {
		t.Error("keyExists says LATHE is absent, but it is present with an empty value")
	}
	if impl.keyExists("DISPLAY", "NOSUCH") {
		t.Error("keyExists says an absent key is present")
	}
	if impl.keyExists("NOSUCH", "WHATEVER") {
		t.Error("keyExists says a key in an absent section is present")
	}
}
