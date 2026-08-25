// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package st

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseRoots are the directories whose every .st file must parse.  Third-party
// corpora live under thirdparty/ with their own licence and a README recording
// provenance; see testdata/README.md.
var parseRoots = []string{
	"testdata/thirdparty/ironplc",
}

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
	if len(files) < 20 {
		t.Fatalf("found only %d .st files under %v; expected the full imported subset",
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
