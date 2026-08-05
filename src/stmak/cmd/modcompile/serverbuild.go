// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Rebuilding stmakd on an installed system.
//
// On such a system nothing that feeds the rebuild may be writable by an
// unprivileged user, and the compiler may not run as root. Those two demands
// are met by splitting the rebuild into three phases across two privilege
// levels:
//
//	root   copy the registered module sources into the derived build tree,
//	       regenerate go.mod and imports_generated.go there   <- the trust decision
//	drop   re-exec as an unprivileged identity: resolve dependencies
//	       and compile into a staging path                    <- compiler never root
//	root   install the staged binary, reapply capabilities    <- irreducibly privileged
//
// Codegen stays in the root phase deliberately: the tree is root:root 0755, so
// an unprivileged phase could not write into it — and it need not, because
// regeneration is modcompile's own string emission and runs no module-supplied
// code. What must never run as root is the phase that does: go build, cgo, gcc.
//
// A run-in-place or build tree has no such split to make. There config.StateDir
// is empty, DerivedBuild is false, and everything below reduces to compiling
// the sources where they sit, exactly as it always did.
//
// See docs/dev/EXTERNAL_MODULE_INSTALL_DESIGN.md sections 2 through 4.3.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/stratuMAK/stratumak/src/stmak/internal/config"
)

const (
	// buildUser is the unprivileged system account the compiler runs as.
	// Created by the package's postinst; it owns nothing but its own cache.
	buildUser = "stratumak-build"

	// buildCacheDir is buildUser's HOME, its Go build and module caches, its
	// scratch copy of the build tree and the staging path for the binary it
	// produces.
	//
	// Named explicitly rather than left to $HOME: sudo configurations differ
	// on whether HOME survives, and the caches must land in neither root's
	// home (where a privileged build would strand them) nor the shared tree
	// (which is root-owned and must stay that way).
	buildCacheDir = "/var/cache/stratumak-build"

	// buildPhaseArg and buildCModPhaseArg are the internal subcommands this
	// binary re-execs itself with for its two unprivileged phases. Not public
	// interfaces: each compiles what the caller names into a path the caller
	// names, with the caller's own privileges, so invoking them directly gains
	// nobody anything.
	buildPhaseArg     = "__build-server"
	buildCModPhaseArg = "__compile-cmod"
)

// buildScratchDir, buildStagingDir and the cache paths below all sit under
// buildCacheDir so that a purge of one directory removes everything the build
// identity owns.
func buildScratchDir() string { return filepath.Join(buildCacheDir, "tree") }
func buildStagingDir() string { return filepath.Join(buildCacheDir, "staging") }
func buildHomeDir() string    { return filepath.Join(buildCacheDir, "home") }
func buildGoCacheDir() string { return filepath.Join(buildCacheDir, "go-build") }
func buildGoModDir() string   { return filepath.Join(buildCacheDir, "go-mod") }

// serverOutputPath returns the path a rebuilt stmakd is installed to.
//
// On a packaged system that is the state directory's copy, which $(bindir)
// only points at: the package-owned binary under $(libexecdir) is never
// written, so an upgrade always lands cleanly and "is this server locally
// modified?" stays a question about one symlink.
func serverOutputPath() string {
	if d := config.LocalBinDir(); d != "" {
		return filepath.Join(d, "stmakd")
	}
	return filepath.Join(config.EMC2BinDir, "stmakd")
}

// requirePrivilege fails with an actionable message when an operation that
// must write root-owned directories is not running as root.
//
// The point is the message. Without it the first thing an administrator sees
// is a bare "mkdir: permission denied" naming a directory they never chose,
// which says nothing about what to do next.
func requirePrivilege(op string) {
	if os.Geteuid() == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "modcompile %s: needs root: try `sudo modcompile %s ...`\n", op, op)
	fmt.Fprintf(os.Stderr,
		"modcompile %s: it writes %s, which is owned by root so that no unprivileged\n"+
			"  user can place build inputs that a later privileged rebuild would compile.\n"+
			"  The compiler itself is not run as root — modcompile drops to %q for that.\n",
		op, config.StateDir(), buildUser)
	os.Exit(1)
}

// buildLock, once acquired, is held for the life of the process; the OS
// releases it at exit. Held in a package variable so a command that reaches
// the lock twice — add-gomod runs the rebuild — does not contend with itself.
var buildLock *os.File

// acquireBuildLock serializes the privileged operations that share the build
// cache, the module registry and the installed binary: rebuild, add-gomod,
// rm-gomod and --install. Fail-fast rather than blocking: a queued operation
// would run against a registry the earlier one is still changing, and the
// administrator is right there to retry. Exits the process on contention —
// every caller is a top-level command.
//
// The lock file lives under the build cache and is created, root-owned, if
// absent; callers gate on the layouts where these operations are privileged,
// so this only ever runs as root.
func acquireBuildLock(op string) {
	if buildLock != nil {
		return
	}
	if err := os.MkdirAll(buildCacheDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile %s: creating %s: %v\n", op, buildCacheDir, err)
		os.Exit(1)
	}
	f, err := lockFile(filepath.Join(buildCacheDir, ".lock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile %s: %v\n", op, err)
		os.Exit(1)
	}
	buildLock = f
}

// lockFile opens (creating if absent) and exclusively flocks path, without
// blocking. The file is never removed: flock serializes on the inode, and
// deleting it would hand the next two callers two different inodes to "lock".
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening the lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf(
				"another stratumak-build operation is running (%s is locked); wait for it to finish and retry", path)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return f, nil
}

// requireCModInstallPrivilege fails early when `--install` cannot write the
// cmod directory, rather than after a successful compile.
//
// Only for a layout that has a state directory: in a run-in-place tree the
// cmod directory is the developer's own and needs no privilege at all.
func requireCModInstallPrivilege() {
	if config.LocalCModDir() == "" || os.Geteuid() == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"modcompile --install: needs root: try `sudo modcompile --install ...`\n"+
			"  It writes %s, which is owned by root.\n"+
			"  Use --compile instead to leave the .so in the current directory.\n",
		config.LocalCModDir())
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Compiling a cmod without being root
// ---------------------------------------------------------------------------

// compileCModStaged compiles one cmod through the same three phases a server
// rebuild uses, and for the same reason: `sudo modcompile --install foo.comp`
// otherwise runs gcc, as root, over source somebody else wrote.
//
// cPath is C that modcompile itself emitted (from a .comp) or the caller's own
// .c file. srcDir is the directory the original source came from, whose headers
// a relative #include may reach.
//
// Parsing and code generation stay with the caller, privileged, on the same
// grounds as the registry codegen: a .comp is data, and turning it into C runs
// no part of it. Only gcc is dropped.
func compileCModStaged(cPath, srcDir, outDir, soName string, extraIncludes []string) error {
	uid, gid, who, err := buildIdentity()
	if err != nil {
		return err
	}
	if err := prepareBuildCache(uid, gid); err != nil {
		return err
	}

	// A directory per module name, wiped each time: two installs of different
	// modules must not see each other's headers, and a header deleted from the
	// source since last time must not linger here.
	//
	// Its parent, staging/cmod, is root-owned (prepareBuildCache above), which
	// is what makes everything root does in here sound: the build identity
	// cannot rename this leaf out from under root's writes, and does not own
	// it either until chownTree hands it over below — after root's last write.
	stage := filepath.Join(buildStagingDir(), "cmod", soName)
	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("clearing %s: %w", stage, err)
	}
	if err := os.MkdirAll(stage, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", stage, err)
	}

	// The C to compile. Root writes it, because root is what could read the
	// original — the build identity has no business in anybody's home
	// directory, and often no access to it either.
	stagedC := filepath.Join(stage, soName+".c")
	if err := copyFile(cPath, stagedC, 0644); err != nil {
		return fmt.Errorf("staging the generated C: %w", err)
	}

	// Headers from the source directory, so a relative #include still
	// resolves. Only headers, and only from that directory: this is standing
	// in for the -I the compile would otherwise have had, not copying a
	// stranger's working directory into a shared cache.
	if srcDir != "" {
		if err := stageLocalHeaders(srcDir, stage); err != nil {
			return err
		}
	}

	if err := normalizeModes(stage); err != nil {
		return fmt.Errorf("making the staged sources readable: %w", err)
	}
	// Handing over is the last thing root does inside the staged tree: every
	// write and chmod above ran while stage and its contents were still
	// root-owned, so the build identity never had a window to swap anything
	// root was about to touch. From here root only reads the result back,
	// through installStaged's O_NOFOLLOW + owner check.
	if err := chownTree(stage, uid, gid); err != nil {
		return fmt.Errorf("handing the staged sources to %s: %w", who, err)
	}

	// The staged directory replaces srcDir on the include path; the rest of
	// the -I list names installed directories the build identity can read.
	args := []string{buildCModPhaseArg, stagedC, stage, soName}
	for _, inc := range extraIncludes {
		if strings.HasPrefix(inc, "-I") && filepath.Clean(inc[2:]) == filepath.Clean(srcDir) {
			continue
		}
		args = append(args, inc)
	}

	fmt.Fprintf(os.Stderr, "Compiling %s as %s...\n", soName, who)
	if err := runAsBuildIdentity(uid, gid, args); err != nil {
		return fmt.Errorf("compiling %s: %w", soName, err)
	}

	staged := filepath.Join(stage, soName+".so")
	out := filepath.Join(outDir, soName+".so")
	if err := installStaged(staged, out, uid, 0644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Installed %s\n", out)
	return nil
}

// stageLocalHeaders copies the headers under srcDir into dst, keeping their
// relative layout so that `#include "sub/local.h"` still resolves.
//
// Headers only. The compile's include path reaches exactly this directory
// tree, so this is what has to come along — and nothing else does, which keeps
// `--install` run from a download directory out of the shared cache.
func stageLocalHeaders(srcDir, dst string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".h" && ext != ".hh" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return copyFile(path, target, 0644)
	})
}

// chownTree hands a whole staged tree to the build identity, which has to own
// what it is about to compile into: the compiler writes its output beside the
// sources.
//
// Children before parents. The moment a directory is handed over, its new
// owner can swap the entries under it for symlinks, and a chown that followed
// such a swap would land on whatever the link points at. Chowning bottom-up
// means every path is still inside root-owned directories when it is visited,
// so that window never opens; os.Lchown for the same reason — nothing root
// staged is a symlink, and one that appeared would be somebody's redirect.
func chownTree(root string, uid, gid int) error {
	var paths []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	// Walk is pre-order (parents first); reversed, every child precedes its
	// parent.
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Lchown(paths[i], uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// phaseMustNotBeRoot is the refusal the unprivileged phases make when invoked
// with euid 0: they exist precisely so that the compiler does not run as root.
// The euid is a parameter so the decision is testable without being root.
func phaseMustNotBeRoot(phase string, euid int) error {
	if euid != 0 {
		return nil
	}
	return fmt.Errorf("modcompile %s: refusing to compile as root; "+
		"this phase exists precisely so that the compiler does not", phase)
}

// cmdCompileCModPhase is the unprivileged half of a cmod install, reached only
// through the re-exec above.
func cmdCompileCModPhase(args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr,
			"modcompile "+buildCModPhaseArg+": expected <c-file> <out-dir> <so-name> [-Idir...]")
		os.Exit(1)
	}
	cPath, outDir, soName := args[0], args[1], args[2]

	if err := phaseMustNotBeRoot(buildCModPhaseArg, os.Geteuid()); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	if err := compileToSO(cPath, outDir, soName, args[3:]); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// The build identity
// ---------------------------------------------------------------------------

// buildIdentity resolves the unprivileged identity the compiler runs as.
//
// The dedicated system account is used whenever it exists — one identity, one
// cache, and identical behaviour whether the rebuild was started from a sudo
// shell, a root shell or automation. The SUDO_UID fallback covers the tree
// that was installed with `make install` instead of from the package, where
// nothing ever created the account. There is deliberately no third case:
// refusing is better than compiling as root.
func buildIdentity() (uid, gid int, name string, err error) {
	if u, lookupErr := user.Lookup(buildUser); lookupErr == nil {
		uid, err = strconv.Atoi(u.Uid)
		if err != nil {
			return 0, 0, "", fmt.Errorf("account %q has a non-numeric uid %q", buildUser, u.Uid)
		}
		gid, err = strconv.Atoi(u.Gid)
		if err != nil {
			return 0, 0, "", fmt.Errorf("account %q has a non-numeric gid %q", buildUser, u.Gid)
		}
		if uid == 0 {
			return 0, 0, "", fmt.Errorf("account %q is uid 0; it exists to not be root", buildUser)
		}
		return uid, gid, buildUser, nil
	}

	sudoUID, sudoGID := os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID")
	if sudoUID != "" && sudoGID != "" {
		uid, err = strconv.Atoi(sudoUID)
		if err != nil {
			return 0, 0, "", fmt.Errorf("SUDO_UID=%q is not a number", sudoUID)
		}
		gid, err = strconv.Atoi(sudoGID)
		if err != nil {
			return 0, 0, "", fmt.Errorf("SUDO_GID=%q is not a number", sudoGID)
		}
		if uid == 0 {
			return 0, 0, "", fmt.Errorf("SUDO_UID is 0; there is no unprivileged identity to drop to")
		}
		who := sudoUID
		if u, lookupErr := user.LookupId(sudoUID); lookupErr == nil {
			who = u.Username
		}
		return uid, gid, who, nil
	}

	return 0, 0, "", fmt.Errorf(
		"no unprivileged identity to build as: the %q account does not exist and "+
			"SUDO_UID is unset.\n"+
			"  The account is created by the package; on a tree installed with "+
			"`make install`, create it with\n"+
			"    sudo adduser --system --group --no-create-home --home %s %s",
		buildUser, buildCacheDir, buildUser)
}

// prepareBuildCache creates the build identity's cache tree.
//
// Two kinds of directory come out of it, and the difference is a security
// boundary. The cache root and the staging directories stay root-owned: root
// later writes, chmods and renames inside them, and no component of a path a
// privileged process operates on may be owned by the build identity — an
// owner can swap a directory for a symlink between any two of root's calls
// and redirect a write, chmod or chown wherever it likes (os.OpenFile,
// os.Chmod and os.Chown all follow symlinks). These are re-asserted on every
// run, top-down, so a cache laid out by an older modcompile is taken back
// before anything trusts it.
//
// The build identity gets the leaves it actually works in: its HOME, the Go
// caches, the scratch tree, plus the per-build staging leaves created and
// handed over (empty, or fully written) elsewhere. Root never writes below
// those, and reads results back only through installStaged's O_NOFOLLOW +
// owner check.
func prepareBuildCache(uid, gid int) error {
	// Top-down: each directory is root-owned before the one inside it is
	// touched, so a hostile rename in a still-unfixed parent has no window.
	for _, d := range rootOwnedCacheDirs() {
		if err := ensureOwnedDir(d, 0, 0); err != nil {
			return err
		}
	}
	// Only directories this call creates are chowned: an existing cache
	// belongs to the build identity already, and walking it on every rebuild
	// would be pure cost.
	for _, d := range buildIdentityCacheDirs() {
		if _, err := os.Stat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return fmt.Errorf("handing %s to the build identity: %w", d, err)
		}
	}
	return nil
}

// rootOwnedCacheDirs lists the directories under the build cache that must
// belong to root, in the order they are taken back: parents strictly before
// children, so that by the time a directory is fixed its parent can no longer
// be renamed out from under it.
func rootOwnedCacheDirs() []string {
	return []string{
		buildCacheDir,
		buildStagingDir(),
		filepath.Join(buildStagingDir(), "cmod"),
	}
}

// buildIdentityCacheDirs lists the directories the build identity owns
// outright — the ones root never writes into.
func buildIdentityCacheDirs() []string {
	return []string{
		buildHomeDir(), buildGoCacheDir(), buildGoModDir(), buildScratchDir(),
	}
}

// ensureOwnedDir makes d a directory owned by uid:gid with mode 0755,
// replacing whatever else sits at that path. A symlink is removed, never
// followed: these are the directories privileged code writes into, and
// following a link somebody else planted is exactly the redirect this
// prevents. (prepareBuildCache always passes uid 0; the parameters exist so
// the decision logic is testable without being root.)
func ensureOwnedDir(d string, uid, gid int) error {
	fi, err := os.Lstat(d)
	switch {
	case err == nil && fi.IsDir():
		if st, ok := fi.Sys().(*syscall.Stat_t); ok &&
			int(st.Uid) == uid && int(st.Gid) == gid && fi.Mode().Perm() == 0o755 {
			return nil
		}
		if err := os.Chmod(d, 0o755); err != nil {
			return fmt.Errorf("setting the mode of %s: %w", d, err)
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return fmt.Errorf("taking ownership of %s: %w", d, err)
		}
		return nil
	case err == nil:
		// A file or a symlink where a directory belongs.
		if err := os.Remove(d); err != nil {
			return fmt.Errorf("removing %s, which should be a directory: %w", d, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("inspecting %s: %w", d, err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", d, err)
	}
	// MkdirAll is subject to the umask; the mode must hold regardless.
	if err := os.Chmod(d, 0o755); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", d, err)
	}
	if err := os.Chown(d, uid, gid); err != nil {
		return fmt.Errorf("taking ownership of %s: %w", d, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The derived build tree
// ---------------------------------------------------------------------------

// syncBuildTree regenerates the build tree from the two things that are
// authoritative: the package-owned stratuMAK sources, and the root-owned copy of
// each registered external module.
//
// Regenerating rather than accumulating is what makes an upgrade correct. The
// pristine sources change under the tree whenever the package is upgraded, and
// a tree that only ever had modules added to it would keep compiling whatever
// release it was first built from.
func syncBuildTree() error {
	pristine := config.StmakDir()
	tree := config.BuildTreeDir()

	if _, err := os.Stat(pristine); err != nil {
		return fmt.Errorf("the installed stratuMAK sources are missing from %s: %w", pristine, err)
	}
	if err := os.MkdirAll(tree, 0755); err != nil {
		return fmt.Errorf("creating the build tree %s: %w", tree, err)
	}

	// Mirror, so anything the previous build left behind that the current
	// sources no longer contain is deleted — external/ included, which is
	// repopulated from the registry immediately below.
	if err := dirMirror(pristine, tree, nil); err != nil {
		return fmt.Errorf("copying the stratuMAK sources into %s: %w", tree, err)
	}

	mods, err := registeredModules()
	if err != nil {
		return err
	}
	for _, name := range mods {
		src := filepath.Join(config.ModuleRegistryDir(), name)
		dst := filepath.Join(tree, "external", name)
		if err := os.MkdirAll(dst, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", dst, err)
		}
		if err := dirMirror(src, dst, nil); err != nil {
			return fmt.Errorf("copying external module %s: %w", name, err)
		}
	}
	return nil
}

// registeredModules lists the names of the external modules recorded in the
// registry, in a stable order. An absent registry is not an error: it is the
// normal state of a system nobody has added a module to.
func registeredModules() ([]string, error) {
	dir := config.ModuleRegistryDir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the module registry %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil // os.ReadDir already sorts by filename
}

// normalizeModes makes a freshly recorded module source readable by the build
// identity: 0644 for files, 0755 for directories, with the execute bit kept
// where the original had one.
//
// Without this a module sitting at 0600 in someone's home directory is copied
// root-owned and unreadable, and the unprivileged compile fails on a file the
// administrator can see perfectly well.
func normalizeModes(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if info.IsDir() {
			mode = 0755
		} else if info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		return os.Chmod(path, mode)
	})
}

// ---------------------------------------------------------------------------
// The privileged orchestrator
// ---------------------------------------------------------------------------

// rebuildServer runs the full three-phase rebuild and installs the result.
// It exits the process on failure — every caller is a top-level command.
func rebuildServer() {
	if !config.DerivedBuild() {
		// In-place layout: the sources are the caller's own and the output
		// goes where the caller can already write. Nothing to split.
		if err := goModTidy(); err != nil {
			fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
			os.Exit(1)
		}
		buildServerInPlace(config.BuildTreeDir(), serverOutputPath())
		return
	}

	requirePrivilege("rebuild")
	acquireBuildLock("rebuild")

	if err := syncBuildTree(); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}

	// Codegen, still privileged: modcompile's own emission into the
	// root-owned tree, with no module-supplied code involved.
	cmdRegenerateGomod()
	cmdRegenerateImports()

	uid, gid, who, err := buildIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}
	if err := prepareBuildCache(uid, gid); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}

	// The staged binary lands in a leaf directory the build identity owns —
	// created fresh by root inside the root-owned staging directory and handed
	// over empty. Root never writes below it, and reads the result back only
	// through installStaged's O_NOFOLLOW + owner check.
	serverStage := filepath.Join(buildStagingDir(), "server")
	if err := os.RemoveAll(serverStage); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: clearing %s: %v\n", serverStage, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(serverStage, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: creating %s: %v\n", serverStage, err)
		os.Exit(1)
	}
	if err := os.Chown(serverStage, uid, gid); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: handing %s to the build identity: %v\n", serverStage, err)
		os.Exit(1)
	}
	staged := filepath.Join(serverStage, "stmakd")

	fmt.Fprintf(os.Stderr, "Building stmakd as %s...\n", who)
	if err := runBuildPhase(uid, gid, config.BuildTreeDir(), staged); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: building stmakd: %v\n", err)
		os.Exit(1)
	}

	// A binary that cannot even print its usage must not reach the installed
	// path: `-h` runs every compiled-in module's init(), which is where a
	// broken external module — one whose name collides with a compiled-in
	// module, above all — panics. Discovering that here costs a re-run;
	// discovering it at the machine's next start costs the controller.
	fmt.Fprintf(os.Stderr, "Smoke-testing stmakd as %s...\n", who)
	if err := smokeTestServer(uid, gid, staged); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}

	out := serverOutputPath()
	if err := installServer(staged, out, uid); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: installing stmakd: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "stmakd installed: %s\n", out)
}

// runBuildPhase re-execs this binary as the unprivileged build identity to do
// the compiling.
//
// Re-exec rather than an in-process setuid. Not for the old one-thread reason
// — syscall.Setuid has applied to every thread since Go 1.16 — but because a
// fresh process is also the only honest way to hand the build a clean
// environment, and it puts a process boundary exactly at the staging hand-off.
func runBuildPhase(uid, gid int, tree, out string) error {
	return runAsBuildIdentity(uid, gid,
		[]string{buildPhaseArg, tree, buildScratchDir(), out})
}

// runAsBuildIdentity re-execs this binary as the given unprivileged identity,
// with an environment stated in full rather than inherited.
//
// Shared by the two phases that must not be privileged — the server rebuild
// and the cmod compile — so that neither can drift into a different notion of
// what "unprivileged" means.
func runAsBuildIdentity(uid, gid int, args []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary to re-exec: %w", err)
	}

	cmd := buildIdentityCmd(uid, gid, self, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// buildIdentityCmd prepares a command running bin as the unprivileged build
// identity, with an environment stated in full rather than inherited. The
// caller wires up stdout/stderr and runs it.
func buildIdentityCmd(uid, gid int, bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
			// No supplementary groups: the build inherits nothing from
			// whatever groups happened to be current.
			Groups:      []uint32{},
			NoSetGroups: false,
		},
	}
	// HOME and the caches must not fall back to root's, GOTOOLCHAIN=local
	// keeps the build on the toolchain LinuxCNC was configured with, and PATH
	// has to be able to find the compiler and the linker. The Go variables are
	// inert for a cmod compile and cost nothing to set.
	cgoC, cgoLD := cgoFlags()
	cmd.Env = []string{
		"HOME=" + buildHomeDir(),
		"GOCACHE=" + buildGoCacheDir(),
		"GOMODCACHE=" + buildGoModDir(),
		"GOTOOLCHAIN=local",
		"GOFLAGS=",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"CGO_CPPFLAGS=" + cgoC,
		"CGO_CFLAGS=" + cgoC,
		"CGO_LDFLAGS=" + cgoLD,
		"CC=" + resolveCC(),
		"CXX=" + resolveCXX(),
	}
	// $STMAK_DIR is absent by construction, and must stay so: it would send the
	// server build phase back to the pristine tree, silently dropping every
	// external module.
	return cmd
}

// smokeTestServer runs the staged binary once, as the build identity, before
// it is allowed anywhere near the installed path. `-h` is enough to be
// decisive: printing the usage still runs every compiled-in module's init(),
// which is where a broken external module fails (the verify script relies on
// the same property). The same re-exec mechanism as the build phases, so
// "unprivileged" cannot mean two different things.
func smokeTestServer(uid, gid int, staged string) error {
	return runSmokeCmd(buildIdentityCmd(uid, gid, staged, "-h"), staged)
}

// runSmokeCmd runs a prepared smoke-test command with its output captured,
// and folds that output into the error: the whole point of the test is the
// message the broken binary died with.
func runSmokeCmd(cmd *exec.Cmd, staged string) error {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"the freshly built stmakd failed its smoke test and was not installed (%s -h: %v). Its output:\n%s",
			staged, err, strings.TrimRight(out.String(), "\n"))
	}
	return nil
}

// cmdBuildPhase is the unprivileged phase, reached only through the re-exec
// above: copy the root-owned tree into a scratch area this identity owns,
// resolve dependencies, compile.
//
// The scratch copy is what lets `go mod tidy` and `go build -mod=mod` write
// go.mod and go.sum without the shared tree ever being writable by anything
// but root.
//
// All three directories are named by the caller rather than derived here: the
// phase then depends on nothing but its arguments, which is what makes it
// runnable on its own to reproduce a failed rebuild.
func cmdBuildPhase(args []string) {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "modcompile "+buildPhaseArg+": expected <tree> <scratch> <output>")
		os.Exit(1)
	}
	tree, scratch, out := args[0], args[1], args[2]

	if err := phaseMustNotBeRoot(buildPhaseArg, os.Geteuid()); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	// The copy is called "stmak", and the name carries meaning: a handful of
	// headers include their way in from the source root outwards
	// ("stmak/generated/gmi/canon/canon_api.h"), which resolves against the
	// tree's parent directory only if the tree is called what those includes
	// say it is. buildServerInPlace passes that parent as an include root.
	treeCopy := filepath.Join(scratch, "stmak")
	if err := os.MkdirAll(treeCopy, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: creating the scratch tree: %v\n", err)
		os.Exit(1)
	}
	if err := dirMirror(tree, treeCopy, nil); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: copying the build tree into %s: %v\n", treeCopy, err)
		os.Exit(1)
	}

	if err := goModTidyIn(treeCopy); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: %v\n", err)
		os.Exit(1)
	}
	buildServerInPlace(treeCopy, out)
}

// installChown is os.Chown, indirected only so the unprivileged tests can
// drive installStagedPrep through its stage→prepare→rename ordering (chowning
// the temp file to root needs CAP_CHOWN). Everything else chowns directly.
var installChown = os.Chown

// installStaged copies a file the unprivileged build identity produced into a
// root-owned location, as root.
//
// This is the one place the trust boundary is crossed in the returning
// direction, so it is the one place that has to be careful about it. The
// staged file sits in a directory that identity owns and can therefore replace
// between any two operations: it is opened once with O_NOFOLLOW, checked
// through that descriptor to be a regular file belonging to the expected uid,
// and read from that same descriptor — never reopened by name.
//
// The destination is written beside itself and renamed, so a failure part-way
// through leaves whatever was already there intact and running.
func installStaged(staged, out string, uid int, mode os.FileMode) error {
	return installStagedPrep(staged, out, uid, mode, nil)
}

// installStagedPrep is installStaged with a hook that runs on the fully
// written temp file, after its mode and ownership are settled and before the
// atomic rename. installServer uses it to setcap the temp file: a rename
// carries the security.capability xattr along, so the binary that appears at
// out is complete — contents and capabilities — in one atomic step, and a
// failure in the hook aborts with the temp removed and the old binary
// untouched.
func installStagedPrep(staged, out string, uid int, mode os.FileMode, prepare func(tmp string) error) error {
	f, err := os.OpenFile(staged, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("opening the staged file %s: %w", staged, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspecting the staged file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the staged file %s is not a regular file", staged)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine the owner of %s", staged)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("the staged file %s belongs to uid %d, expected the build identity %d",
			staged, st.Uid, uid)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(out), err)
	}

	tmp := out + ".new"
	_ = os.Remove(tmp)
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := io.Copy(dst, f); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	// os.OpenFile applies the umask; the result must have the mode asked for
	// regardless of the umask the administrator happened to run sudo under.
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := installChown(tmp, 0, 0); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("taking ownership of %s: %w", tmp, err)
	}
	if prepare != nil {
		if err := prepare(tmp); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", out, err)
	}
	return nil
}

// installServer moves the staged binary into place and reapplies the file
// capabilities the previous one carried.
func installServer(staged, out string, uid int) error {
	// Capabilities are read before the replacement, and through whatever
	// symlink chain $(bindir) currently resolves to: on a system nobody has
	// rebuilt yet, `out` is a symlink onto the package-owned binary and its
	// capabilities are the ones to carry over.
	oldCaps := getFileCaps(out)
	if oldCaps == "" {
		// The package's own binary is the reference for what this server is
		// supposed to hold, so fall back to it. That covers the machine whose
		// previous rebuild already lost them: without this the loss is
		// permanent, since every later rebuild copies the same nothing
		// forward and only a hand-written setcap gets realtime back.
		if pristine := config.PristineServerPath(); pristine != "" && pristine != out {
			oldCaps = getFileCaps(pristine)
			if oldCaps != "" {
				fmt.Fprintf(os.Stderr,
					"modcompile: %s had no file capabilities; taking them from %s\n", out, pristine)
			}
		}
	}

	// The capabilities go onto the staged temp file, before the rename: the
	// rename carries the security.capability xattr along, so there is no
	// moment when the installed path holds a server without its capabilities
	// — and no moment after which a failed setcap has already destroyed the
	// old binary. A setcap failure aborts the install outright, temp removed,
	// old binary still in place.
	var prepare func(string) error
	if oldCaps != "" {
		caps := oldCaps
		prepare = func(tmp string) error { return applyFileCaps(tmp, caps) }
	} else {
		fmt.Fprintf(os.Stderr,
			"modcompile: warning: %s carried no file capabilities, so none were applied.\n"+
				"  Realtime will not start until they are; on a packaged system they are\n"+
				"  applied by the package's postinst.\n", out)
	}
	return installStagedPrep(staged, out, uid, 0755, prepare)
}
