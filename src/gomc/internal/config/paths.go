// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
// Package config provides compile-time path configuration for gomc-server.
// Variables are set via -ldflags -X at build time, matching the @VAR@ substitutions
// in the legacy linuxcnc.in bash script.
package config

import (
	"os"
	"path/filepath"
)

// Compile-time path variables. These are set via -ldflags when building:
//
//	-X 'github.com/sittner/linuxcnc/src/gomc/config.EMC2Home=/usr/lib/linuxcnc'
var (
	// EMC2Home is the top-level LinuxCNC installation directory (@EMC2_HOME@).
	EMC2Home string

	// EMC2BinDir is the directory containing LinuxCNC binaries (@EMC2_BIN_DIR@).
	EMC2BinDir string

	// EMC2TclDir is the directory containing LinuxCNC Tcl files (@EMC2_TCL_DIR@).
	EMC2TclDir string

	// EMC2HelpDir is the directory containing LinuxCNC help files (@EMC2_HELP_DIR@).
	EMC2HelpDir string

	// EMC2RtlibDir is the directory containing LinuxCNC realtime libraries (@EMC2_RTLIB_DIR@).
	EMC2RtlibDir string

	// EMC2CmodDir is the directory containing C plugin modules (@EMC2_CMOD_DIR@).
	EMC2CmodDir string

	// EMC2CmodIncludeDir is the directory containing cmod header files (gomc_*.h).
	// For RIP this is src/gomc/pkg/cmodule, for installed it's include/linuxcnc/cmod.
	EMC2CmodIncludeDir string

	// EMC2GomcDir is the directory containing the gomc Go module source,
	// always baked as the INSTALLED location ($(datadir)/linuxcnc/gomc).
	// External Go modules need it to compile against the same module.
	//
	// Do not read this directly — call GomcDir(), which lets $GOMC_DIR
	// override it. The build itself needs the source tree, which does not
	// exist at the installed path until `make install` has run, and a
	// run-in-place tree needs its own copy; both set $GOMC_DIR
	// (scripts/rip-environment, and the modcompile recipes in
	// src/gomc/Submakefile).
	EMC2GomcDir string

	// GoBinary is the path to the Go compiler used to build LinuxCNC.
	// External Go plugins must use the same Go toolchain for compatibility.
	GoBinary string

	// CCompiler is the C compiler LinuxCNC was configured with ($(CC),
	// may include arguments). modcompile uses it to build cmods and as
	// cgo CC when rebuilding gomc-server; the CC environment variable
	// still takes precedence.
	CCompiler string

	// CxxCompiler is the C++ compiler LinuxCNC was configured with
	// ($(CXX), may include arguments). Used as cgo CXX when rebuilding
	// gomc-server; the CXX environment variable still takes precedence.
	CxxCompiler string

	// EMC2ConfigPath is the colon-separated list of config search directories (@LINUXCNC_CONFIG_PATH@).
	EMC2ConfigPath string

	// EMC2NCFilesDir is the directory containing LinuxCNC NC files (@EMC2_NCFILES_DIR@).
	EMC2NCFilesDir string

	// EMC2LangDir is the directory containing LinuxCNC language files (@EMC2_LANG_DIR@).
	EMC2LangDir string

	// EMC2ImageDir is the directory containing LinuxCNC images (@EMC2_IMAGE_DIR@).
	EMC2ImageDir string

	// EMC2TclLibDir is the directory containing LinuxCNC Tcl library files (@EMC2_TCL_LIB_DIR@).
	EMC2TclLibDir string

	// HalibDir is the directory containing HAL library files (@HALLIB_DIR@).
	HalibDir string

	// EMC2WebAppDir is the directory containing web application static files.
	// Web apps are served from subdirectories: <EMC2WebAppDir>/<app-name>/
	// For RIP this is share/gomc/webapp, for installed it's $(datadir)/gomc/webapp.
	EMC2WebAppDir string

	// EMC2Version is the LinuxCNC version string (@EMC2VERSION@).
	EMC2Version string

	// RunInPlace indicates whether LinuxCNC is being run from the build directory ("yes" or "no") (@RUN_IN_PLACE@).
	RunInPlace string

	// Tclsh is the path to the Tcl shell interpreter (@TCLSH@).
	// Falls back to looking up "tclsh" on PATH when empty.
	Tclsh string

	// ModExt is the module file extension for loadable kernel modules (@MODEXT@).
	ModExt string

	// KernelVers is the required kernel version string (@KERNEL_VERS@).
	KernelVers string

	// BuildFlags is a comma-separated list of enabled build tags (e.g.
	// "ADSSERVER,CLASSICLADDER,HALSCOPE").  Used by modcompile to filter
	// conditional entries in packages.conf.
	BuildFlags string

	// EMC2StateDir is the root of everything an administrator can change
	// after installation ($(localstatedir)/lib/stratumak). It holds the
	// registered external module sources, the build tree derived from them,
	// a locally rebuilt server and locally built cmods.
	//
	// It exists because none of that may live under a directory the package
	// manager owns: an upgrade would silently discard it. Empty for a
	// run-in-place tree, which has no packaged/mutable split — see StateDir().
	EMC2StateDir string

	// EMC2LibexecDir is where the pristine, package-owned gomc-server lives
	// ($(libexecdir)/stratumak). $(bindir)/gomc-server is a symlink chain onto
	// it; a local rebuild replaces a link in that chain and never this file,
	// so an upgrade of the package is live the moment dpkg unpacks it.
	// Empty for a run-in-place tree.
	EMC2LibexecDir string
)

// GomcDirEnv is the environment variable that overrides the baked-in
// EMC2GomcDir. It names the directory outright rather than being derived from
// a tree root, so it cannot silently resolve to the wrong place if some other
// "where is LinuxCNC" variable is set. The same name is what modcompile hands
// to out-of-tree cmod projects as the GOMC_DIR make variable.
const GomcDirEnv = "GOMC_DIR"

// GomcDir returns the gomc Go module source directory: $GOMC_DIR when set,
// otherwise the installed location baked in at build time.
//
// The build and a run-in-place tree both set $GOMC_DIR because the baked value
// points at a path that only exists after `make install`.
func GomcDir() string {
	if d := os.Getenv(GomcDirEnv); d != "" {
		return d
	}
	return EMC2GomcDir
}

// StateDir returns the root of the mutable post-install state, or "" for a
// layout that has none — a run-in-place tree, where the whole tree already
// belongs to the developer.
//
// "" is the discriminator, not a fallback: callers branch on it rather than
// substituting some other directory, because there is no other directory that
// would be correct. See EXTERNAL_MODULE_INSTALL_DESIGN.md section 4.1.
func StateDir() string { return EMC2StateDir }

// ModuleRegistryDir returns the directory holding one root-owned copy of each
// registered external module's source, keyed by module name.
//
// This, not the build tree, is the source of truth: the build tree is
// regenerated from the pristine gomc sources plus these copies, so "is the
// build tree trustworthy" reduces to "did every entry here arrive through a
// privileged, attributable step". Empty when StateDir is.
func ModuleRegistryDir() string {
	if EMC2StateDir == "" {
		return ""
	}
	return filepath.Join(EMC2StateDir, "modules")
}

// LocalBinDir returns the directory holding a locally rebuilt gomc-server.
// $(bindir)/gomc-server points here; the entry is a symlink onto the pristine
// binary until a local rebuild replaces it with a real file. Empty when
// StateDir is.
func LocalBinDir() string {
	if EMC2StateDir == "" {
		return ""
	}
	return filepath.Join(EMC2StateDir, "bin")
}

// LocalCModDir returns the directory holding locally built cmods, searched by
// the launcher in addition to EMC2CmodDir. Empty when StateDir is, in which
// case EMC2CmodDir is the only cmod directory.
func LocalCModDir() string {
	if EMC2StateDir == "" {
		return ""
	}
	return filepath.Join(EMC2StateDir, "cmod")
}

// PristineServerPath returns the package-owned gomc-server, or "" for a layout
// that has no pristine copy distinct from the installed one.
func PristineServerPath() string {
	if EMC2LibexecDir == "" {
		return ""
	}
	return filepath.Join(EMC2LibexecDir, "gomc-server")
}

// DerivedBuild reports whether a rebuild of gomc-server must go through the
// derived build tree under StateDir rather than compiling the gomc sources
// where they sit.
//
// $GOMC_DIR wins: it names a tree the caller has chosen outright — the build
// itself does this, and so does a run-in-place tree — and in that tree the
// sources are both authoritative and writable, so there is nothing to derive.
func DerivedBuild() bool {
	return EMC2StateDir != "" && os.Getenv(GomcDirEnv) == ""
}

// BuildTreeDir returns the directory a rebuild of gomc-server compiles from:
// the derived tree under StateDir when DerivedBuild, otherwise GomcDir itself.
func BuildTreeDir() string {
	if DerivedBuild() {
		return filepath.Join(EMC2StateDir, "gomc")
	}
	return GomcDir()
}
