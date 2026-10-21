// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pkgreg"
)

// withInPlaceTree points modcompile at an empty run-in-place tree: the module
// store is <tree>/external, the build tree is the tree itself, and stmakd
// would land in <tree>/bin.
func withInPlaceTree(t *testing.T) string {
	t.Helper()
	tree := t.TempDir()
	oldState, oldBin := config.EMC2StateDir, config.EMC2BinDir
	t.Cleanup(func() { config.EMC2StateDir, config.EMC2BinDir = oldState, oldBin })
	config.EMC2StateDir = ""
	config.EMC2BinDir = filepath.Join(tree, "bin")
	t.Setenv(config.StmakDirEnv, tree)
	return tree
}

// buildServer stands in for a rebuild that linked the generated packages:
// it writes a stmakd newer than anything generated so far.
func buildServer(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(config.EMC2BinDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.EMC2BinDir, "stmakd")
	if err := os.WriteFile(path, []byte("#!/bin/false\n"), 0755); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

// writeIDL writes a minimal valid IDL for api into dir as file.
func writeIDL(t *testing.T, dir, file, api string) string {
	t.Helper()
	src := "@api " + api + "\n" +
		"@author \"Test\"\n" +
		"@license \"GPL Version 2\"\n" +
		"@version 1\n" +
		"@prefix " + api + "\n" +
		"@rest_export true\n\n" +
		"type State {\n    count: u32\n}\n\n" +
		"@method \"GET\"\n@path \"/state\"\n@rt_safe \"false\"\n" +
		"func get_state() -> State\n"
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAddGmiRecordsAndGenerates(t *testing.T) {
	tree := withInPlaceTree(t)
	src := t.TempDir()
	idl := writeIDL(t, src, "transp.gmi", "transp")

	if _, err := addGmi(idl, false); err != nil {
		t.Fatalf("addGmi: %v", err)
	}
	entry := filepath.Join(tree, "external", "gmi-transp")
	if _, err := os.Stat(filepath.Join(entry, "gmi", "transp.gmi")); err != nil {
		t.Fatalf("IDL not recorded: %v", err)
	}
	origin, _ := os.ReadFile(filepath.Join(entry, ".origin"))
	if strings.TrimSpace(string(origin)) != idl {
		t.Errorf(".origin = %q, want %q", origin, idl)
	}

	// Not a Go module: the entry's root has no Go files.
	if got := pkgreg.DiscoverExternal(tree); len(got) != 0 {
		t.Errorf("DiscoverExternal = %v, want none", got)
	}

	if err := generateExternalGMI(tree); err != nil {
		t.Fatalf("generateExternalGMI: %v", err)
	}
	pkg := filepath.Join(entry, "gmi", "transp")
	for _, f := range []string{"transp_api.h", "transp_cgo.go"} {
		if _, err := os.Stat(filepath.Join(pkg, f)); err != nil {
			t.Errorf("%s not generated: %v", f, err)
		}
	}

	want := pkgreg.Entry{Type: pkgreg.TypeGMI, ImportPath: "external/gmi-transp/gmi/transp"}
	if got := pkgreg.DiscoverGMI(tree); !slices.Contains(got, want) {
		t.Errorf("DiscoverGMI = %v, want it to contain %v", got, want)
	}

	if got := gmiIncludeDir("transp"); got != pkg {
		t.Errorf("gmiIncludeDir = %q, want %q", got, pkg)
	}
	if got := gmiIncludeDir("nosuchapi"); got != "" {
		t.Errorf("gmiIncludeDir(unknown) = %q, want \"\"", got)
	}
}

func TestAllGMIIncludeDirs(t *testing.T) {
	tree := withInPlaceTree(t)
	builtin := filepath.Join(tree, "generated", "gmi", "kins")
	if err := os.MkdirAll(builtin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtin, "kins_api.h"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := addGmi(writeIDL(t, t.TempDir(), "transp.gmi", "transp"), false); err != nil {
		t.Fatal(err)
	}
	if err := generateExternalGMI(tree); err != nil {
		t.Fatal(err)
	}

	got := allGMIIncludeDirs()
	for _, want := range []string{builtin, filepath.Join(tree, "external", "gmi-transp", "gmi", "transp")} {
		if !slices.Contains(got, want) {
			t.Errorf("allGMIIncludeDirs = %v, want it to contain %s", got, want)
		}
	}
}

func TestAddGmiRefusesBuiltin(t *testing.T) {
	tree := withInPlaceTree(t)
	builtin := filepath.Join(tree, "generated", "gmi", "kins")
	if err := os.MkdirAll(builtin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtin, "kins_api.h"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := addGmi(writeIDL(t, t.TempDir(), "kins.gmi", "kins"), false)
	if err == nil || !strings.Contains(err.Error(), "built into") {
		t.Fatalf("addGmi of a built-in API: err = %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "external", "gmi-kins")); err == nil {
		t.Error("a refused IDL was recorded anyway")
	}
}

func TestAddGmiOrigin(t *testing.T) {
	withInPlaceTree(t)
	first := writeIDL(t, t.TempDir(), "transp.gmi", "transp")
	second := writeIDL(t, t.TempDir(), "transp.gmi", "transp")

	if _, err := addGmi(first, false); err != nil {
		t.Fatal(err)
	}
	if _, err := addGmi(first, false); err != nil {
		t.Errorf("re-adding from the same origin: %v", err)
	}
	if _, err := addGmi(second, false); err == nil {
		t.Error("adding the same API from another origin without --force succeeded")
	}
	if _, err := addGmi(second, true); err != nil {
		t.Errorf("--force: %v", err)
	}
}

func TestAddGmiRefusesInvalidIDL(t *testing.T) {
	tree := withInPlaceTree(t)
	path := filepath.Join(t.TempDir(), "bad.gmi")
	if err := os.WriteFile(path, []byte("@api bad\n@version 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := addGmi(path, false); err == nil {
		t.Fatal("an IDL without @license was accepted")
	}
	if entries, _ := os.ReadDir(filepath.Join(tree, "external")); len(entries) != 0 {
		t.Errorf("store holds %d entries after a refusal, want 0", len(entries))
	}
}

func TestGenerateExternalGMIRefusesDuplicates(t *testing.T) {
	tree := withInPlaceTree(t)
	for _, mod := range []string{"gmi-one", "gmi-two"} {
		dir := filepath.Join(tree, "external", mod, "gmi")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		writeIDL(t, dir, "transp.gmi", "transp")
	}
	err := generateExternalGMI(tree)
	if err == nil || !strings.Contains(err.Error(), "provided by both") {
		t.Fatalf("err = %v, want a duplicate refusal", err)
	}
}

func TestAddGmiUnchanged(t *testing.T) {
	tree := withInPlaceTree(t)
	idl := writeIDL(t, t.TempDir(), "transp.gmi", "transp")

	if changed, err := addGmi(idl, false); err != nil || !changed {
		t.Fatalf("first add: changed=%v err=%v, want a change", changed, err)
	}
	// Recorded but not generated yet: still a change, the rebuild is due.
	if changed, err := addGmi(idl, false); err != nil || !changed {
		t.Fatalf("re-add before a rebuild: changed=%v err=%v, want a change", changed, err)
	}
	if err := generateExternalGMI(tree); err != nil {
		t.Fatal(err)
	}
	// Generated but stmakd never built from it (the rebuild failed after
	// codegen): still a change.
	if changed, err := addGmi(idl, false); err != nil || !changed {
		t.Fatalf("re-add after a failed build: changed=%v err=%v, want a change", changed, err)
	}
	if err := generateExternalGMI(tree); err != nil {
		t.Fatal(err)
	}
	buildServer(t)
	if changed, err := addGmi(idl, false); err != nil || changed {
		t.Fatalf("re-add of the built IDL: changed=%v err=%v, want none", changed, err)
	}
	src, _ := os.ReadFile(idl)
	if err := os.WriteFile(idl, append(src, []byte("# edited\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err := addGmi(idl, false); err != nil || !changed {
		t.Fatalf("re-add of an edited IDL: changed=%v err=%v, want a change", changed, err)
	}
}

func TestAddGmiRefusesStreamIDL(t *testing.T) {
	tree := withInPlaceTree(t)
	src := "@api tap\n@author \"Test\"\n@license \"GPL Version 2\"\n@version 1\n@prefix tap\n\n" +
		"stream_server tap {\n    new_conn(conn_id: u32) -> i32\n    closed_conn(conn_id: u32)\n}\n"
	path := filepath.Join(t.TempDir(), "tap.gmi")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := addGmi(path, false)
	if err == nil || !strings.Contains(err.Error(), "stream_server") {
		t.Fatalf("addGmi of a stream IDL: err = %v, want a refusal", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(tree, "external")); len(entries) != 0 {
		t.Errorf("store holds %d entries after a refusal, want 0", len(entries))
	}
}

// TestGenerateExternalGMILeavesGomodIDLs: an IDL an external Go module ships
// comes with its own generated package; regenerate must not touch it.
func TestGenerateExternalGMILeavesGomodIDLs(t *testing.T) {
	tree := withInPlaceTree(t)
	dir := filepath.Join(tree, "external", "mymod", "gmi")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeIDL(t, dir, "transp.gmi", "transp")
	if err := generateExternalGMI(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transp")); err == nil {
		t.Error("a Go module's own IDL was regenerated")
	}
	// It still owns the name, though.
	_, err := addGmi(writeIDL(t, t.TempDir(), "transp.gmi", "transp"), false)
	if err == nil || !strings.Contains(err.Error(), "mymod") {
		t.Errorf("add-gmi of an API a Go module provides: err = %v, want a refusal naming it", err)
	}
}

func TestAddGmiRenamedAPI(t *testing.T) {
	tree := withInPlaceTree(t)
	src := t.TempDir()
	idl := writeIDL(t, src, "transp.gmi", "transp")
	if _, err := addGmi(idl, false); err != nil {
		t.Fatal(err)
	}
	// The same file now declares another API: the old entry must go.
	writeIDL(t, src, "transp.gmi", "transport")
	if changed, err := addGmi(idl, false); err != nil || !changed {
		t.Fatalf("re-add after a rename: changed=%v err=%v", changed, err)
	}
	entries, _ := os.ReadDir(filepath.Join(tree, "external"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"gmi-transport"}) {
		t.Errorf("store = %v, want [gmi-transport]", names)
	}
}

// TestAddGmiStaleOrigin: an entry whose origin file is gone is replaced
// without --force; a live one from elsewhere is not.
func TestAddGmiStaleOrigin(t *testing.T) {
	withInPlaceTree(t)
	gone := t.TempDir()
	first := writeIDL(t, gone, "transp.gmi", "transp")
	if _, err := addGmi(first, false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	second := writeIDL(t, t.TempDir(), "transp.gmi", "transp")
	if _, err := checkGmi(second, false); err != nil {
		t.Errorf("checkGmi against a stale entry: %v", err)
	}
	if changed, err := addGmi(second, false); err != nil || !changed {
		t.Fatalf("replacing a stale entry: changed=%v err=%v", changed, err)
	}
}

// TestCheckGmiTouchesNothing: the check add-gmi runs over every file before
// recording any must not write to the store.
func TestCheckGmiTouchesNothing(t *testing.T) {
	tree := withInPlaceTree(t)
	if _, err := checkGmi(writeIDL(t, t.TempDir(), "transp.gmi", "transp"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree, "external")); err == nil {
		t.Error("checkGmi created the store")
	}
}

// A run-in-place tree only has $STMAK_DIR when rip-environment was sourced.
// Without it the pristine sources are not found, but the build has copied
// every built-in header into the flat include directory: the API is still
// built in, so a .comp consuming it compiles and an external IDL cannot take
// its name.
func TestBuiltinGMIDirFlatIncludeFallback(t *testing.T) {
	home := t.TempDir()
	inc := filepath.Join(home, "include")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inc, "mcode_handler_api.h"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	oldHome, oldRIP := config.EMC2Home, config.RunInPlace
	t.Cleanup(func() { config.EMC2Home, config.RunInPlace = oldHome, oldRIP })
	config.EMC2Home, config.RunInPlace = home, "yes"
	t.Setenv(config.StmakDirEnv, t.TempDir()) // a stmak tree without generated/

	if got := builtinGMIDir("mcode_handler"); got != inc {
		t.Errorf("builtinGMIDir(mcode_handler) = %q, want %q", got, inc)
	}
	if got := gmiIncludeDir("mcode_handler"); got != inc {
		t.Errorf("gmiIncludeDir(mcode_handler) = %q, want %q", got, inc)
	}
	if got := builtinGMIDir("nosuchapi"); got != "" {
		t.Errorf("builtinGMIDir(nosuchapi) = %q, want \"\"", got)
	}
}
