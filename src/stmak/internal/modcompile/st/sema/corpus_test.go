// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package sema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// acceptCorpus is testdata/accept relative to this package.
const acceptCorpus = "../testdata/accept"

// corpusStatus records, for each first-party fixture, whether the frontend
// lowers it -- and if not, which staged feature it is waiting for.
//
// This table was the ledger of what was left to build.  Every entry is now
// empty, so it has become the assertion that the language did not shrink: a
// fixture that stops lowering fails here with the diagnostic that stopped it.
// A feature added later takes a new fixture and a new entry.
var corpusStatus = map[string]string{
	// Lowered by phase 2: scalars, one PROGRAM, the statement set.
	"and2":     "",
	"scale":    "",
	"literals": "",
	"loops":    "",

	// Lowered by phase 4: user-defined types and arrays.
	"statemachine": "",
	"structs":      "",
	"types":        "",
	"arrays":       "",

	// Lowered by phase 5: function blocks and functions.
	"ramplimit": "",
	"fbnest":    "",
	"funcs":     "",

	// Lowered by phase 6: shared state and several programs.
	"twoprograms": "",

	// Explicit pin mapping with AT, at any depth.
	"atmap": "",

	// Named constants folding at their use sites, and qualified enumeration
	// constants -- both found by measuring against a real project.
	"constants": "",

	// Every documentation field, so the man page can be checked whole.
	"documented": "",

	// Named pieces of a body, in both the shapes they take.
	"actions": "",
	"params":  "",
	"period":  "",
}

// TestAcceptCorpusStatus checks every first-party fixture against the ledger.
func TestAcceptCorpusStatus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(acceptCorpus, "*.st"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures under %s: %v", acceptCorpus, err)
	}
	if len(files) != len(corpusStatus) {
		t.Fatalf("%d fixtures but %d ledger entries; a fixture was added or removed "+
			"without updating corpusStatus", len(files), len(corpusStatus))
	}

	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".st")
		t.Run(name, func(t *testing.T) {
			want, known := corpusStatus[name]
			if !known {
				t.Fatalf("fixture %s is not in corpusStatus; add it", name)
			}

			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			file, perr := st.Parse(path, string(src))
			if perr != nil {
				t.Fatalf("parse failed:\n%v", perr)
			}

			pkg, cerr := Check(name, file)

			if want == "" {
				if cerr != nil {
					t.Fatalf("expected this fixture to lower, got:\n%v", cerr)
				}
				// A lowered fixture must be a usable package, not merely an
				// error-free one.
				if err := pkg.Validate(); err != nil {
					t.Errorf("lowered package fails its own invariant: %v", err)
				}
				if len(pkg.Component.Pins) == 0 {
					t.Error("lowered to a component with no pins")
				}
				if len(pkg.Component.Functions) == 0 || pkg.Component.Functions[0].Body == nil {
					t.Error("lowered to a component with no function body")
				}
				if pkg.Component.Summary == "" {
					t.Error("summary did not reach the component")
				}
				return
			}

			if cerr == nil {
				t.Fatalf("this fixture lowered, but corpusStatus still says it is waiting for %q. "+
					"If the feature landed, clear its entry", want)
			}
			if !strings.Contains(cerr.Error(), want) {
				t.Errorf("expected a diagnostic containing %q, got:\n%v", want, cerr)
			}
		})
	}
}

// TestStagedRejectionsNameTheirPhase checks that every "not supported yet"
// diagnostic tells the reader when it arrives.  A refusal that does not say
// what is missing sends someone looking for a mistake in their own source.
func TestStagedRejectionsNameTheirPhase(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(acceptCorpus, "*.st"))
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".st")
		if corpusStatus[name] == "" {
			continue
		}
		src, _ := os.ReadFile(path)
		file, perr := st.Parse(path, string(src))
		if perr != nil {
			continue
		}
		_, cerr := Check(name, file)
		if cerr == nil {
			continue
		}
		msg := cerr.Error()
		if !strings.Contains(msg, "phase") && !strings.Contains(msg, "arrives with") {
			t.Errorf("%s: refusal does not say when the feature arrives:\n%s", name, msg)
		}
	}
}
