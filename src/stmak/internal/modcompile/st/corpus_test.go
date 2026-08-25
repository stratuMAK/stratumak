// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseRoots are the directories whose every .st file must parse.
//
// accept/ is first-party: valid stratuMAK components written as
// specification by example.  Sema will additionally have to accept them, and
// phases 3 onward compile and run them; today the obligation is that they
// parse.
//
// thirdparty/ is imported, with each corpus keeping its own licence and a
// README recording provenance -- see testdata/README.md.  Those files parse
// but will not pass sema, by design.
var parseRoots = []string{
	"testdata/accept",
	"testdata/thirdparty/ironplc",
}

// acceptRoot holds the first-party fixtures.  They are checked more strictly
// than the imported ones: an accept fixture that declares no PROGRAM is not a
// component, whatever else it may be.
const acceptRoot = "testdata/accept"

// TestParseCorpus is the phase 1 gate: every file in the parse corpus parses
// without a syntax error.
//
// These are parse-level fixtures.  Most reference identifiers that are never
// declared, because upstream uses them to test parsing -- valid syntax,
// invalid programs.  That is exactly what this test asserts and no more; sema
// will reject most of them once it exists, which is correct and is why they
// are not in an accept tier.
func TestParseCorpus(t *testing.T) {
	files := collectST(t, parseRoots)
	if len(files) == 0 {
		t.Fatal("parse corpus is empty; expected the imported IronPLC fixtures")
	}
	// A tripwire on the corpus size: if the files move, this must fail rather
	// than silently pass over nothing.
	if len(files) < 30 {
		t.Fatalf("found only %d .st files under %v; expected the first-party and imported corpora",
			len(files), parseRoots)
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			f, err := Parse(path, string(src))
			if err != nil {
				t.Fatalf("parse failed:\n%v", err)
			}
			if f == nil {
				t.Fatal("parse returned no file and no error")
			}
			// Every fixture declares at least one thing.  A file that parses
			// to nothing means the scanner swallowed it -- an unterminated
			// comment consuming the source, say -- which would otherwise look
			// like a pass.
			if len(f.Types)+len(f.Blocks)+len(f.Funcs)+len(f.Programs)+len(f.Globals) == 0 {
				t.Error("parsed to an empty file: no declarations recognised")
			}
		})
	}
}

// TestAcceptCorpusShape checks that the first-party fixtures are components
// rather than fragments.  A file here is meant to become a loadable module in
// phase 3, so one that declares no PROGRAM has drifted from its purpose --
// which a parse-only test would not notice.
func TestAcceptCorpusShape(t *testing.T) {
	files := collectST(t, []string{acceptRoot})
	if len(files) == 0 {
		t.Fatal("no first-party accept fixtures found")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			f, err := Parse(path, string(src))
			if err != nil {
				t.Fatalf("parse failed:\n%v", err)
			}
			if len(f.Programs) == 0 {
				t.Error("declares no PROGRAM, so it is not a component")
			}
			// Every component gets a summary: docgen builds a man page for
			// each one, and a component with no summary produces a useless
			// page.  Catching that here keeps it out of the shipped corpus.
			var hasSummary bool
			for _, a := range f.Attrs {
				if a.Name == "summary" {
					hasSummary = true
				}
			}
			if !hasSummary {
				t.Error("no {attribute 'summary'} pragma; docgen needs one for the man page")
			}
			// A PROGRAM with no VAR_INPUT and no VAR_OUTPUT has no HAL surface
			// at all, which cannot be what a fixture means.
			for _, prog := range f.Programs {
				var pins int
				for _, vb := range prog.Vars {
					if vb.Kind.IsHAL() {
						pins += len(vb.Decls)
						continue
					}
					// A plain VAR with an AT clause is HAL-facing too.
					for _, d := range vb.Decls {
						if d.Located != "" {
							pins += len(d.Names)
						}
					}
				}
				if pins == 0 {
					t.Errorf("PROGRAM %s declares no HAL-facing variables", prog.Name)
				}
			}
		})
	}
}

func collectST(t *testing.T, roots []string) []string {
	t.Helper()
	var files []string
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !fi.IsDir() && strings.HasSuffix(p, ".st") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	return files
}
