// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
	"github.com/stratuMAK/stratumak/src/stmak/internal/pkgreg"
)

// withInPlaceTree points modcompile at an empty run-in-place tree: the module
// store is <tree>/external and the build tree is the tree itself.
func withInPlaceTree(t *testing.T) string {
	t.Helper()
	tree := t.TempDir()
	oldState := config.EMC2StateDir
	t.Cleanup(func() { config.EMC2StateDir = oldState })
	config.EMC2StateDir = ""
	t.Setenv(config.StmakDirEnv, tree)
	return tree
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
	for _, mod := range []string{"one", "two"} {
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
