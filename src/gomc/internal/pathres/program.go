// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sittner/linuxcnc/src/gomc/internal/config"
)

// G-code programs are user data, not configuration: they live under
// [DISPLAY]PROGRAM_PREFIX and [RS274NGC]SUBROUTINE_PATH, deliberately outside
// the config directory.  Containing them within the config roots would break
// ordinary machine use, so they get their own root set — the one ngcpreview
// already shipped for its get_file endpoint (user ruling, 2026-07-22),
// plus the system NC-files directory so the stock demos stay openable:
//
//	PROGRAM_PREFIX + SUBROUTINE_PATH + <EMC2_HOME>/share + <EMC2_NCFILES_DIR>
//
// Every entry point that opens a program by name off the wire resolves against
// it: ngcpreview get_file/gen_preview and task program_open.

// ProgramDirs returns the allowed G-code directories.
//
// get reads an INI value (pass a namespaced getter to honour per-instance
// sections); it may be nil when the launcher runs without an INI, in which
// case only the system directories (the share directory and the install-tree
// nc_files directory) are allowed.  baseDir anchors relative INI values — the
// INI file's directory.
func ProgramDirs(get func(section, key string) string, baseDir string) []string {
	var dirs []string

	resolve := func(p string) string {
		if p == "" {
			return ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		// EvalSymlinks resolves ".." and symlinks; fall back to Abs for a
		// directory that does not exist yet.
		if abs, err := filepath.EvalSymlinks(p); err == nil {
			return abs
		}
		abs, _ := filepath.Abs(p)
		return abs
	}

	if get != nil {
		// [DISPLAY]PROGRAM_PREFIX — the NC_FILES directory.
		if d := resolve(get("DISPLAY", "PROGRAM_PREFIX")); d != "" {
			dirs = append(dirs, d)
		}
		// [RS274NGC]SUBROUTINE_PATH — colon-separated list.
		for _, p := range strings.Split(get("RS274NGC", "SUBROUTINE_PATH"), ":") {
			if d := resolve(strings.TrimSpace(p)); d != "" {
				dirs = append(dirs, d)
			}
		}
	}

	// Where the controller writes filtered output. A [FILTER] program turns a
	// source file into G-code, and the interpreter, get_file and the preview
	// all have to be able to read the result — so the directory holding it is
	// a program root like any other. It is server-owned: nothing but the
	// filter runner ever writes there.
	dirs = append(dirs, FilteredDir())

	// System share directory (splash-screen NGC files and friends).
	if config.EMC2Home != "" {
		if d := resolve(filepath.Join(config.EMC2Home, "share")); d != "" {
			dirs = append(dirs, d)
		}
	}

	// System NC-files directory — the demo/sample programs shipped with
	// LinuxCNC (nc_files/3dtest.ngc and friends).  This is its own build-time
	// path (@EMC2_NCFILES_DIR@) that need not live under EMC2Home, so it is
	// added explicitly rather than derived from it.  A config that keeps its
	// [DISPLAY]PROGRAM_PREFIX elsewhere would otherwise be unable to open the
	// stock demos.
	if d := resolve(config.EMC2NCFilesDir); d != "" {
		dirs = append(dirs, d)
	}

	return dirs
}

// ProgramResolver returns a resolver for G-code paths: the shared base rule
// plus the program directories as extra roots.
//
// It returns nil when no default resolver has been published, so callers fail
// loudly rather than silently opening an unchecked path.
func ProgramResolver(get func(section, key string) string, baseDir string) *Resolver {
	base := Default()
	if base == nil {
		return nil
	}
	return base.WithRoots(ProgramDirs(get, baseDir)...)
}

// Canonical returns the single spelling of a program path that every API
// surface uses, so that comparing two of them for equality answers "the same
// file?" rather than "the same characters?".
//
// The paths a client sees come from three places — the task's loaded program,
// the file a motion segment came from, and the preview's file table — and they
// have to be comparable: an AXIS deciding whether the line it is about to
// highlight belongs to the program it is showing does exactly that comparison.
// The interpreter reaches sub-files through find_ngc_file, which composes them
// from SUBROUTINE_PATH and can hand back a relative name, so without one rule
// the same file arrives spelled several ways.
//
// Absolute, cleaned, symlinks resolved. A path that cannot be canonicalised —
// most often one that does not exist yet — falls back to its absolute cleaned
// form rather than failing: an approximate identity is still better than none,
// and the caller has no useful recovery.
func Canonical(p string) string {
	if p == "" {
		return ""
	}
	abs := p
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// FilteredDir is where the controller keeps the G-code it produced from
// filtered source files.
//
// Derived from the process id rather than created on demand, because two
// separate modules need the same answer: the task writes the filtered program
// there, and ngcpreview reads it back through get_file and gen_preview. They
// are different instances of different packages in one process, so the path
// has to be computable, not passed around.
//
// This is only the shared parent: each task instance works exclusively inside
// its own FilteredInstanceDir below it, so multiple milltasks in one process
// never touch each other's output.
func FilteredDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("gomc-filtered-%d", os.Getpid()))
}

// FilteredInstanceDir is one task instance's private slice of FilteredDir.
// The instance creates it before its first conversion and removes it at
// shutdown; the shared parent above it is left for whichever instance stops
// last.
func FilteredInstanceDir(instance string) string {
	return filepath.Join(FilteredDir(), instance)
}

// InFilteredDir reports whether path — canonical, per Canonical — lies inside
// the filtered-output tree. The task uses it to refuse to re-filter a file
// that already IS a filter's product: a client that converted client-side
// hands over its result under the source's extension, and matching that
// extension against [FILTER] a second time would convert G-code that has
// already been converted.
func InFilteredDir(path string) bool {
	root := FilteredDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return under(path, root)
}

// EnsureFilteredDir creates dir (a FilteredInstanceDir) and its shared
// parent, refusing to adopt anything this process does not own. The path
// under os.TempDir() is predictable and the server may hold file
// capabilities (cap_dac_override): silently accepting a pre-created
// directory — or a symlink posing as one — would hand whoever planted it
// every filtered program written there, and let them substitute the G-code
// the interpreter then executes. Anything already present must be a real
// directory owned by this uid, or the open fails loudly.
func EnsureFilteredDir(dir string) error {
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := ensureOwnedDir(d); err != nil {
			return err
		}
	}
	return nil
}

func ensureOwnedDir(d string) error {
	err := os.Mkdir(d, 0o700)
	if err == nil || !os.IsExist(err) {
		return err
	}
	// Lstat, not Stat: a symlink planted here must be seen as the symlink it
	// is, never followed to a legitimate-looking target.
	fi, lerr := os.Lstat(d)
	if lerr != nil {
		return lerr
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s exists and is not a directory; refusing to use it", d)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by this process's uid %d; refusing to use it", d, os.Getuid())
	}
	if fi.Mode().Perm() != 0o700 {
		if cerr := os.Chmod(d, 0o700); cerr != nil {
			return cerr
		}
	}
	return nil
}
