// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package stlib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gmiast "github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/gmicompile/parser"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// A .comp that consumes a GMI interface never reads the .gmi: cgen includes
// the generated header by name and the C compiler does the rest.  Structured
// Text has to read it, because it type-checks the calls itself -- which is
// also what lets it refuse a call to a function the interface does not mark
// rt_safe, a check nothing performed before.
//
// The interfaces are found the way libraries are, and for the same reason: a
// project keeping its own beside its components should need no configuration.

// GMIPathEnv holds extra interface directories, colon-separated.
const GMIPathEnv = "STMAK_GMI_IDL_PATH"

// GMIDirs is the search path for interfaces used by a source at srcPath.
func GMIDirs(srcPath string, shipped ...string) []string {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv(GMIPathEnv)) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	if d := filepath.Dir(srcPath); d != "" {
		dirs = append(dirs, d)
	}
	for _, d := range shipped {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// APIsUsedBy reads the file-level pragmas naming GMI interfaces and loads each
// one, keyed by lower-cased name.
//
// Both directions are loaded the same way: a provider needs the interface to
// check its methods against, a consumer to check its calls.
func APIsUsedBy(f *st.File, dirs []string) (map[string]*gmiast.API, error) {
	names := map[string]bool{}
	for _, a := range f.Attrs {
		switch strings.ToLower(a.Name) {
		case "gmi_provide":
			for _, n := range splitList(a.Value) {
				names[strings.ToLower(n)] = true
			}
		case "gmi_consume":
			for _, e := range splitList(a.Value) {
				// `home from homing` -- the provider instance is the loader's
				// business only in that it is not part of the name.
				api, _, _ := strings.Cut(e, " from ")
				names[strings.ToLower(strings.TrimSpace(api))] = true
			}
		}
	}
	if len(names) == 0 {
		return nil, nil
	}

	out := make(map[string]*gmiast.API, len(names))
	for name := range names {
		path, err := findGMI(name, dirs)
		if err != nil {
			return nil, err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		api, perrs := parser.Parse(path, string(src))
		if len(perrs) > 0 {
			return nil, fmt.Errorf("%s: %s", path, strings.Join(perrs, "\n"))
		}
		out[strings.ToLower(api.Name)] = api
	}
	return out, nil
}

// splitList splits a comma-separated attribute value.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// findGMI locates name.gmi on the search path.
func findGMI(name string, dirs []string) (string, error) {
	for _, dir := range dirs {
		p := filepath.Join(dir, name+".gmi")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	where := "nowhere to look"
	if len(dirs) > 0 {
		where = "looked in " + strings.Join(dirs, ", ")
	}
	return "", fmt.Errorf("no GMI interface named %s: %s; set %s to add a directory",
		name, where, GMIPathEnv)
}
