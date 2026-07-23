// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package pathres

import (
	"path/filepath"
	"strings"

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

	// [FILTER]PROGRAM_EXTENSION lines define filtered file types; the filtered
	// output typically goes to a tempdir, but PROGRAM_PREFIX already covers it.

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
