// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Package pathres resolves configuration-supplied filesystem paths.
//
// gomc splits the CLI/REST client from the server, so a path in a module
// argument or a configuration file has no client-side meaning: it is always a
// *server-side* path.  Every such path goes through one rule here, the same
// rule the startup HAL-file processing uses:
//
//  1. a leading "~"/"~/" expands to the user's home directory
//  2. a "LIB:" prefix resolves against the library directories only
//  3. a relative path resolves against the base directory first, then each
//     library directory
//  4. an absolute path is used as given
//  5. the result must lie inside one of the allowed roots (base + library
//     directories) after symlinks are resolved — otherwise it is rejected
//
// The base directory is the INI file's directory; the launcher chdirs to it at
// startup, so it is also the process working directory.  With no INI file
// (halrun mode) the base is the working directory itself.
//
// Containment is deliberate: `load` is reachable over REST, so a configuration
// value must not be able to name /etc/shadow.  It is not the primary access
// control — that is the REST auth seam — but it keeps a configuration mistake
// or a stray argument from turning into an arbitrary file read or write.
//
// Not every configuration string that names something is a path.  Device nodes
// (`spidev_path=/dev/spidev1.0`, `port=/dev/ttyUSB0`), hardware config strings
// (`hm2_pci config=num_encoders=…`) and EtherCAT FoE filenames must NOT be
// resolved here — see PATH_RESOLUTION_INVENTORY.md, category D.  Resolution is
// opt-in per call site for exactly that reason.
package pathres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode selects how a path is checked once it has been located.
type Mode int

const (
	// Read requires an existing regular file.  A directory that name-matches
	// is rejected rather than returned (2.9's linuxcnc.in does the same:
	// `[ -d $foundfile ] && foundmsg=""`), because it only fails more
	// confusingly further down.
	Read Mode = iota

	// Write requires the parent directory to exist and be contained; the file
	// itself need not exist yet.
	Write

	// Dir requires a directory, or a contained parent in which one can be
	// created.
	Dir
)

func (m Mode) String() string {
	switch m {
	case Read:
		return "read"
	case Write:
		return "write"
	case Dir:
		return "dir"
	}
	return "unknown"
}

// Resolver resolves configuration paths against a base directory and a set of
// library directories, and enforces containment within them.
//
// A Resolver is read-only after construction and safe for concurrent use.
type Resolver struct {
	base    string   // absolute; the INI directory, or the cwd when there is no INI
	libDirs []string // absolute; from HALLIB_PATH
	roots   []string // absolute, symlink-resolved; base + libDirs
}

// New builds a Resolver.
//
// baseDir is the INI file's directory; pass "" to use the process working
// directory (halrun mode has no INI).  halibPath is the colon-separated
// HALLIB_PATH.  A "." entry in halibPath is dropped: the working directory is
// already the base, and a relative root cannot be containment-checked
// meaningfully.
func New(baseDir, halibPath string) (*Resolver, error) {
	if baseDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("path resolver: working directory: %w", err)
		}
		baseDir = wd
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("path resolver: base directory %q: %w", baseDir, err)
	}

	r := &Resolver{base: base}
	for _, dir := range strings.Split(halibPath, ":") {
		dir = strings.TrimSpace(dir)
		// "." is the base by definition, and an empty entry would resolve to
		// the working directory by accident.
		if dir == "" || dir == "." {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		r.libDirs = append(r.libDirs, abs)
	}

	// Roots are compared against symlink-resolved candidates, so resolve them
	// too.  A root that does not exist yet cannot contain anything and is
	// simply kept in its lexical form.
	for _, dir := range append([]string{base}, r.libDirs...) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			r.roots = append(r.roots, real)
		} else {
			r.roots = append(r.roots, dir)
		}
	}
	return r, nil
}

// WithRoots returns a copy of r that also searches and allows dirs.
//
// Not every path a machine touches is a *configuration* path.  G-code programs
// live under [DISPLAY]PROGRAM_PREFIX and [RS274NGC]SUBROUTINE_PATH, which are
// deliberately outside the config directory, so a consumer of those declares
// its own roots on top of the shared base rule instead of inventing a second
// resolver.
func (r *Resolver) WithRoots(dirs ...string) *Resolver {
	out := &Resolver{base: r.base}
	out.libDirs = append(out.libDirs, r.libDirs...)
	out.roots = append(out.roots, r.roots...)
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		out.libDirs = append(out.libDirs, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			out.roots = append(out.roots, real)
		} else {
			out.roots = append(out.roots, abs)
		}
	}
	return out
}

// Base returns the directory relative paths resolve against.
func (r *Resolver) Base() string { return r.base }

// Roots returns the allowed containment roots.
func (r *Resolver) Roots() []string {
	out := make([]string, len(r.roots))
	copy(out, r.roots)
	return out
}

// Resolve locates name and returns an absolute, contained path.
//
// The returned path is the located path, not its symlink-resolved form: the
// caller should open the name it was given, while containment is judged on
// where that name actually leads.
func (r *Resolver) Resolve(name string, mode Mode) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("path resolver: empty path")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("path resolver: path contains a NUL byte")
	}

	name = expandTilde(name)

	// A relative write/dir target resolves under the base directory only.  The
	// library directories are a *search* path for reading: writing to one
	// because the name happened to exist there — say `outfile=core.hal`
	// overwriting the system core.hal — is never what the caller meant.
	if mode != Read && !strings.HasPrefix(name, "LIB:") {
		cand := filepath.Clean(name)
		if !filepath.IsAbs(cand) {
			cand = filepath.Join(r.base, name)
		}
		if !matches(cand, mode, true) {
			return "", fmt.Errorf("path resolver: %s (%s) is not a usable target", cand, mode)
		}
		if err := r.contains(cand, mode); err != nil {
			return "", err
		}
		return cand, nil
	}

	var candidates []string

	switch {
	case strings.HasPrefix(name, "LIB:"):
		// LIB: is explicitly "from the library directories", never the config
		// directory.
		lib := strings.TrimPrefix(name, "LIB:")
		if lib == "" {
			return "", fmt.Errorf("path resolver: empty LIB: path")
		}
		for _, dir := range r.libDirs {
			candidates = append(candidates, filepath.Join(dir, lib))
		}
	case filepath.IsAbs(name):
		candidates = []string{filepath.Clean(name)}
	default:
		candidates = append(candidates, filepath.Join(r.base, name))
		for _, dir := range r.libDirs {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}

	var firstContainErr error
	for _, cand := range candidates {
		if !matches(cand, mode, false) {
			continue
		}
		if err := r.contains(cand, mode); err != nil {
			// Keep looking: an earlier search directory may hold a legitimate
			// file even if this candidate escapes.  Report the containment
			// failure only if nothing else works.
			if firstContainErr == nil {
				firstContainErr = err
			}
			continue
		}
		return cand, nil
	}
	if firstContainErr != nil {
		return "", firstContainErr
	}
	return "", fmt.Errorf("path resolver: %s (%s) not found in %s",
		name, mode, strings.Join(r.searchDesc(name), ", "))
}

// searchDesc describes where Resolve looked, for the error message.
func (r *Resolver) searchDesc(name string) []string {
	if strings.HasPrefix(name, "LIB:") {
		return r.libDirs
	}
	if filepath.IsAbs(name) {
		return []string{"the given absolute path"}
	}
	return append([]string{r.base}, r.libDirs...)
}

// matches reports whether cand satisfies the mode's existence requirements.
// allowCreate permits a target that does not exist yet, provided its parent
// directory does.
func matches(cand string, mode Mode, allowCreate bool) bool {
	info, err := os.Stat(cand)
	if err == nil {
		switch mode {
		case Read, Write:
			// Refuse device nodes, FIFOs and sockets: opening a FIFO blocks
			// forever, and the launcher holds modMu across a whole module
			// load, so that would wedge every load, unload and shutdown.
			return info.Mode().IsRegular()
		case Dir:
			return info.IsDir()
		}
		return false
	}
	if !allowCreate {
		return false
	}
	parent, err := os.Stat(filepath.Dir(cand))
	return err == nil && parent.IsDir()
}

// contains reports whether cand lies inside one of the allowed roots, judged
// after symlink resolution so a link cannot be used to step outside.
func (r *Resolver) contains(cand string, mode Mode) error {
	// For a path that does not exist yet (a write target), judge the parent —
	// EvalSymlinks fails on a missing leaf.
	probe := cand
	if _, err := os.Lstat(cand); err != nil {
		probe = filepath.Dir(cand)
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return fmt.Errorf("path resolver: %s: %w", cand, err)
	}
	if probe != cand {
		real = filepath.Join(real, filepath.Base(cand))
	}
	for _, root := range r.roots {
		if under(real, root) {
			return nil
		}
	}
	return fmt.Errorf("path resolver: %s (%s) resolves to %s, which is outside the allowed directories (%s)",
		cand, mode, real, strings.Join(r.roots, ", "))
}

// under reports whether path is root itself or lies beneath it.
func under(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// expandTilde reproduces the `$INIVAR -tildeexpand` step the legacy
// linuxcnc.in applies before resolution: a leading "~" or "~/" becomes the
// user's home directory.  "~user" forms are left untouched (rare and not
// portably resolvable here).
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
