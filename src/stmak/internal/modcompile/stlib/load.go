// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package stlib resolves the `USING <library>;` declarations of a Structured
// Text source and merges what those libraries declare into the file being
// compiled.
//
// A library is a `.st` holding TYPE, FUNCTION and FUNCTION_BLOCK declarations
// and no PROGRAM, marked by a file-level `{attribute 'library' := 'name'}`.
// It is not a shared object: what it declares is generated into the consumer's
// own module, so a call to a library block is a static C function in the same
// translation unit, type-checked against the real declaration and mapped back
// to the library's own source by #line.  See
// docs/dev/MODCOMPILE_ST_STDLIB.md §2 for why, at length.
//
// Merging happens before sema, which then sees one file and neither knows nor
// cares that some of it came from elsewhere.  Positions carry the file they
// came from, so a diagnostic against a library declaration names the library.
package stlib

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/ast"
	"github.com/stratuMAK/stratumak/src/stmak/internal/modcompile/st"
)

// PathEnv is the environment variable holding extra library directories,
// colon-separated, searched before anything else.
const PathEnv = "STMAK_ST_LIB_PATH"

// LibraryAttr marks a file as a library rather than a component.
const LibraryAttr = "library"

// Loader resolves libraries by name.
type Loader struct {
	// Dirs are searched in order, and the first match wins.  A library found
	// earlier shadows one found later, which is what lets a project override
	// a shipped one without moving it out of the way.
	Dirs []string

	// Loaded records the file each library name was taken from, in the order
	// they were read, for --verbose and for diagnostics.
	Loaded []Resolved
}

// Resolved is one library and where it was found.
type Resolved struct {
	Name string
	Path string
}

// DefaultDirs is the search path for a source at srcPath: the environment
// first, then the source's own directory, then the shipped directories.
//
// The source's own directory comes before the shipped ones so that a project
// keeping its libraries beside its components works with no configuration at
// all, which is the case that should need none.
func DefaultDirs(srcPath string, shipped ...string) []string {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv(PathEnv)) {
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

// Load parses the file at path and merges in every library it uses,
// transitively.  It returns the merged file, which is what sema checks.
func (l *Loader) Load(path string) (*st.File, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := st.Parse(path, string(src))
	if err != nil {
		return nil, err
	}
	if name, ok := libraryName(root); ok {
		return nil, fmt.Errorf("%s: %s is a library (it carries {attribute 'library' := '%s'}), "+
			"so it cannot be compiled into a component of its own. "+
			"Compile a file that uses it instead", path, filepath.Base(path), name)
	}
	if err := l.merge(root, path); err != nil {
		return nil, err
	}
	return root, nil
}

// merge resolves root's USING declarations into it, depth first.
func (l *Loader) merge(root *st.File, rootPath string) error {
	seen := map[string]bool{}   // library name -> already merged
	onPath := map[string]bool{} // library name -> being merged, for the cycle check

	var visit func(uses []*st.UseDecl, fromPath string, chain []string) error
	visit = func(uses []*st.UseDecl, fromPath string, chain []string) error {
		for _, u := range uses {
			k := strings.ToLower(u.Name)
			if onPath[k] {
				// A library that uses itself, directly or round a ring.
				// Merging it would not terminate.
				return fmt.Errorf("%s: library %s uses itself: %s",
					u.Pos, u.Name, strings.Join(append(chain, u.Name), " -> "))
			}
			if seen[k] {
				// Already merged through another path.  Using the same
				// library twice is not an error; merging it twice would be,
				// because every declaration would collide with itself.
				continue
			}

			libPath, err := l.find(u.Name)
			if err != nil {
				return fmt.Errorf("%s: %s", u.Pos, err)
			}
			libSrc, err := os.ReadFile(libPath)
			if err != nil {
				return fmt.Errorf("%s: reading library %s: %w", u.Pos, u.Name, err)
			}
			lib, perr := st.Parse(libPath, string(libSrc))
			if perr != nil {
				return perr
			}

			declared, ok := libraryName(lib)
			if !ok {
				return fmt.Errorf("%s: %s is not a library: it has no "+
					"{attribute 'library' := '%s'}, so compiling it would try to make a "+
					"component of it", u.Pos, libPath, u.Name)
			}
			if !ast.EqualName(declared, u.Name) {
				return fmt.Errorf("%s: %s declares itself as library %q, not %q; "+
					"a library's attribute and its file name have to agree or "+
					"USING would find the wrong file", u.Pos, libPath, declared, u.Name)
			}
			if len(lib.Programs) > 0 {
				return fmt.Errorf("%s:%d: a library may not declare a PROGRAM: %s is a "+
					"HAL function, and a library has no component to export it from",
					libPath, lib.Programs[0].Pos.Line, lib.Programs[0].Name)
			}

			onPath[k] = true
			if err := visit(lib.Uses, libPath, append(chain, u.Name)); err != nil {
				return err
			}
			onPath[k] = false

			seen[k] = true
			l.Loaded = append(l.Loaded, Resolved{Name: u.Name, Path: libPath})

			// The declarations, not the attributes: a library's summary and
			// licence are its own and must not become the component's.
			root.Types = append(root.Types, lib.Types...)
			root.Blocks = append(root.Blocks, lib.Blocks...)
			root.Funcs = append(root.Funcs, lib.Funcs...)
			if len(lib.Globals) > 0 {
				return fmt.Errorf("%s:%d: a library may not declare VAR_GLOBAL: "+
					"it would become per-instance state of whatever component used it, "+
					"which is not something a library can reason about",
					libPath, lib.Globals[0].Pos.Line)
			}
		}
		return nil
	}

	return visit(root.Uses, rootPath, nil)
}

// find locates a library's source file.
func (l *Loader) find(name string) (string, error) {
	for _, dir := range l.Dirs {
		p := filepath.Join(dir, name+".st")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	// Case-insensitively too, since ST is: a library written USING Timers
	// should find timers.st rather than report that nothing is there.
	for _, dir := range l.Dirs {
		if p, ok := findCaseless(dir, name); ok {
			return p, nil
		}
	}
	where := "nowhere to look"
	if len(l.Dirs) > 0 {
		where = "looked in " + strings.Join(l.Dirs, ", ")
	}
	return "", fmt.Errorf("no library named %s: %s. "+
		"A library is a .st file of that name carrying {attribute 'library' := '%s'}; "+
		"set %s to add a directory", name, where, name, PathEnv)
}

// findCaseless looks for name.st in dir, ignoring case.
func findCaseless(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	want := strings.ToLower(name) + ".st"
	var hits []string
	for _, e := range entries {
		if !e.IsDir() && strings.ToLower(e.Name()) == want {
			hits = append(hits, filepath.Join(dir, e.Name()))
		}
	}
	if len(hits) == 0 {
		return "", false
	}
	// Deterministic when a case-insensitive filesystem is not in play and two
	// spellings exist side by side.
	sort.Strings(hits)
	return hits[0], true
}

// libraryName reads the {attribute 'library' := 'name'} pragma, if any.
func libraryName(f *st.File) (string, bool) {
	for _, a := range f.Attrs {
		if strings.EqualFold(a.Name, LibraryAttr) {
			return a.Value, true
		}
	}
	return "", false
}

// IsLibrary reports whether the source at path declares itself a library,
// without merging anything.  The build uses it to leave library files alone
// rather than trying to make a module of each.
func IsLibrary(path string) bool {
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	f, err := st.Parse(path, string(src))
	if err != nil {
		return false
	}
	_, ok := libraryName(f)
	return ok
}
