// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package cgen

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/comp"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// genFSMGolden generates testdata/fsm.comp as the build would, with #line
// directives.
func genFSMGolden(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("testdata/fsm.comp")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := comp.Parse("testdata/fsm.comp", string(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pkg.Warnings) != 0 {
		t.Errorf("warnings: %q", pkg.Warnings)
	}
	var buf bytes.Buffer
	if err := GenerateTo(&buf, pkg, "testdata/fsm.c"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return buf.String()
}

// TestGenerate_FSMGolden pins the C an fsm block lowers to.  Regenerate with
// go test ./internal/modcompile/cgen -run FSMGolden -update, and read the
// diff: it is the execution model.
func TestGenerate_FSMGolden(t *testing.T) {
	out := genFSMGolden(t)
	if *update {
		if err := os.WriteFile("testdata/fsm.c", []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/fsm.c")
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Errorf("generated C differs from testdata/fsm.c; rerun with -update and review the diff")
	}
	checkGeneratedC(t, "fsm", out)
}

// Every fsm fragment is bracketed: the directive returning to the generated
// file must name the line that physically follows it.
func TestGenerate_FSMLineDirectivesResume(t *testing.T) {
	out := genFSMGolden(t)
	toComp, back := 0, 0
	for i, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, `#line `) && strings.HasSuffix(line, `"testdata/fsm.comp"`) {
			toComp++
			continue
		}
		var n int
		if _, err := fmt.Sscanf(line, `#line %d "testdata/fsm.c"`, &n); err != nil {
			continue
		}
		back++
		if want := i + 2; n != want {
			t.Errorf("resume directive on physical line %d says line %d; want %d", i+1, n, want)
		}
	}
	if toComp == 0 || toComp != back {
		t.Errorf("%d directives into the .comp, %d back out", toComp, back)
	}
}

// Without an output name the fsm code carries no directives either.
func TestGenerate_FSMNoLineDirectivesWithoutOutputName(t *testing.T) {
	src, err := os.ReadFile("testdata/fsm.comp")
	if err != nil {
		t.Fatal(err)
	}
	if out := gen(t, string(src)); strings.Contains(out, "#line") {
		t.Errorf("Generate emitted #line directives without an output name")
	}
}
