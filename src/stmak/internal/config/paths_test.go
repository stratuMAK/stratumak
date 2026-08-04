// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package config_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// currentValues is the one place the package's variables are read by name.
// TestPathsDefaultValues cross-checks it against the parsed declarations, so
// adding a variable to paths.go without adding it here fails the test.
func currentValues() map[string]string {
	return map[string]string{
		"EMC2Home":           config.EMC2Home,
		"EMC2BinDir":         config.EMC2BinDir,
		"EMC2TclDir":         config.EMC2TclDir,
		"EMC2HelpDir":        config.EMC2HelpDir,
		"EMC2RtlibDir":       config.EMC2RtlibDir,
		"EMC2CmodDir":        config.EMC2CmodDir,
		"EMC2CmodIncludeDir": config.EMC2CmodIncludeDir,
		"EMC2StmakDir":       config.EMC2StmakDir,
		"GoBinary":           config.GoBinary,
		"CCompiler":          config.CCompiler,
		"CxxCompiler":        config.CxxCompiler,
		"EMC2ConfigPath":     config.EMC2ConfigPath,
		"EMC2NCFilesDir":     config.EMC2NCFilesDir,
		"EMC2LangDir":        config.EMC2LangDir,
		"EMC2ImageDir":       config.EMC2ImageDir,
		"EMC2TclLibDir":      config.EMC2TclLibDir,
		"HalibDir":           config.HalibDir,
		"EMC2WebAppDir":      config.EMC2WebAppDir,
		"EMC2Version":        config.EMC2Version,
		"RunInPlace":         config.RunInPlace,
		"Tclsh":              config.Tclsh,
		"ModExt":             config.ModExt,
		"KernelVers":         config.KernelVers,
		"BuildFlags":         config.BuildFlags,
		"EMC2StateDir":       config.EMC2StateDir,
		"EMC2LibexecDir":     config.EMC2LibexecDir,
	}
}

const (
	pathsFile      = "paths.go"
	submakefile    = "../../Submakefile"
	ldflagsPkgVar  = "STMAK_LDFLAGS_PKG"
	ldflagsPkgPath = "github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

// declaredVars parses paths.go and returns the names of the package-level
// variables it declares. Go has no reflection over package-level vars, so the
// source is the only way to enumerate them — which is also what keeps the
// checks below from silently going stale when a variable is added.
func declaredVars(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, pathsFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", pathsFile, err)
	}

	names := make(map[string]bool)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// Every path variable is an untyped-string declaration with no
			// initializer; a value here would defeat -ldflags -X entirely
			// (the linker only patches vars initialized to the zero value).
			if vs.Values != nil {
				for _, n := range vs.Names {
					t.Errorf("%s: var %s has an initializer; -ldflags -X cannot patch it", pathsFile, n.Name)
				}
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "string" {
				for _, n := range vs.Names {
					t.Errorf("%s: var %s is not a string; -ldflags -X only supports strings", pathsFile, n.Name)
				}
			}
			for _, n := range vs.Names {
				names[n.Name] = true
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("no package-level vars found in %s", pathsFile)
	}
	return names
}

var ldflagsXRe = regexp.MustCompile(`-X '\$\(([A-Z_]+)\)\.([A-Za-z0-9_]+)=`)

// injectedVars returns the variable names the build injects via -ldflags -X,
// checking that each one is qualified with STMAK_LDFLAGS_PKG rather than a
// literal (and possibly stale) package path.
func injectedVars(t *testing.T) map[string]bool {
	t.Helper()

	data, err := os.ReadFile(submakefile)
	if err != nil {
		t.Fatalf("reading %s: %v", submakefile, err)
	}
	text := string(data)

	if !strings.Contains(text, ldflagsPkgVar+" := "+ldflagsPkgPath) {
		t.Errorf("%s: %s does not point at %s — the -X flags would target a package that no longer exists",
			submakefile, ldflagsPkgVar, ldflagsPkgPath)
	}

	names := make(map[string]bool)
	for _, m := range ldflagsXRe.FindAllStringSubmatch(text, -1) {
		if m[1] != ldflagsPkgVar {
			t.Errorf("%s: -X targets $(%s).%s, want $(%s)", submakefile, m[1], m[2], ldflagsPkgVar)
		}
		names[m[2]] = true
	}
	if len(names) == 0 {
		t.Fatalf("no -X injections found in %s", submakefile)
	}
	return names
}

// TestLdflagsInjectionTargetsExist is the drift guard for the build-time path
// injection. `go build -ldflags -X pkg.Name=v` silently does NOTHING when Name
// does not exist in pkg — no warning, no error — so a renamed or removed
// variable leaves the build green while the value it was supposed to carry is
// empty at runtime. (This test was written after finding exactly that: the
// Submakefile injected a `DefaultNmlFile` that no Go code has ever declared, an
// NML-era leftover that stmak does not use.)
func TestLdflagsInjectionTargetsExist(t *testing.T) {
	declared := declaredVars(t)
	injected := injectedVars(t)

	var missing []string
	for name := range injected {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s injects -X values for variables that %s does not declare: %v\n"+
			"the linker discards these silently; declare them or drop the -X flag",
			submakefile, pathsFile, missing)
	}
}

// TestUninjectedVarsAreDocumented catches the other direction: a declared
// variable nothing injects is always the empty string in a real build. That is
// legitimate only when the code documents a fallback (Tclsh looks the binary up
// on PATH), so each one must say so.
func TestUninjectedVarsAreDocumented(t *testing.T) {
	declared := declaredVars(t)
	injected := injectedVars(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, pathsFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", pathsFile, err)
	}

	docs := make(map[string]string)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Doc == nil {
				continue
			}
			for _, n := range vs.Names {
				docs[n.Name] = vs.Doc.Text()
			}
		}
	}

	for name := range declared {
		if injected[name] {
			continue
		}
		doc := strings.ToLower(docs[name])
		if !strings.Contains(doc, "fall") && !strings.Contains(doc, "empty") {
			t.Errorf("config.%s is never injected by the build and its doc comment does not "+
				"describe a fallback for the empty value", name)
		}
	}
}

// TestPathsDefaultValues verifies that the compile-time path variables default
// to empty strings when the test binary is built without -ldflags. Driven off
// the parsed declarations so a newly added variable is covered automatically —
// the previous hand-maintained list had drifted to 15 of 24.
func TestPathsDefaultValues(t *testing.T) {
	for name, val := range currentValues() {
		if val != "" {
			t.Errorf("config.%s = %q, want empty string (not set via -ldflags)", name, val)
		}
	}
	// Cross-check the hand-written accessor map against the source, so a new
	// variable cannot slip past the loop above unnoticed.
	values := currentValues()
	for name := range declaredVars(t) {
		if _, ok := values[name]; !ok {
			t.Errorf("config.%s is declared in %s but missing from currentValues() in this test", name, pathsFile)
		}
	}
}
