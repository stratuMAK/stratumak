// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Rebuilding gomc-server on an installed system.
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
// See EXTERNAL_MODULE_INSTALL_DESIGN.md sections 2 through 4.3.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/sittner/linuxcnc/src/gomc/internal/config"
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

	// buildPhaseArg is the internal subcommand this binary re-execs itself
	// with for the unprivileged phase. Not a public interface: it compiles a
	// tree the caller names into a path the caller names, with the caller's
	// own privileges, so invoking it directly gains nobody anything.
	buildPhaseArg = "__build-server"
)

// buildScratchDir, buildStagingDir and the cache paths below all sit under
// buildCacheDir so that a purge of one directory removes everything the build
// identity owns.
func buildScratchDir() string { return filepath.Join(buildCacheDir, "tree") }
func buildStagingDir() string { return filepath.Join(buildCacheDir, "staging") }
func buildHomeDir() string    { return filepath.Join(buildCacheDir, "home") }
func buildGoCacheDir() string { return filepath.Join(buildCacheDir, "go-build") }
func buildGoModDir() string   { return filepath.Join(buildCacheDir, "go-mod") }

// serverOutputPath returns the path a rebuilt gomc-server is installed to.
//
// On a packaged system that is the state directory's copy, which $(bindir)
// only points at: the package-owned binary under $(libexecdir) is never
// written, so an upgrade always lands cleanly and "is this server locally
// modified?" stays a question about one symlink.
func serverOutputPath() string {
	if d := config.LocalBinDir(); d != "" {
		return filepath.Join(d, "gomc-server")
	}
	return filepath.Join(config.EMC2BinDir, "gomc-server")
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

// prepareBuildCache creates the build identity's cache tree and hands it over.
// Only directories this call creates are chowned: an existing cache belongs to
// the build identity already, and walking it on every rebuild would be pure
// cost.
func prepareBuildCache(uid, gid int) error {
	for _, d := range []string{
		buildCacheDir,
		buildHomeDir(), buildGoCacheDir(), buildGoModDir(),
		buildScratchDir(), buildStagingDir(),
	} {
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

// ---------------------------------------------------------------------------
// The derived build tree
// ---------------------------------------------------------------------------

// syncBuildTree regenerates the build tree from the two things that are
// authoritative: the package-owned gomc sources, and the root-owned copy of
// each registered external module.
//
// Regenerating rather than accumulating is what makes an upgrade correct. The
// pristine sources change under the tree whenever the package is upgraded, and
// a tree that only ever had modules added to it would keep compiling whatever
// release it was first built from.
func syncBuildTree() error {
	pristine := config.GomcDir()
	tree := config.BuildTreeDir()

	if _, err := os.Stat(pristine); err != nil {
		return fmt.Errorf("the installed gomc sources are missing from %s: %w", pristine, err)
	}
	if err := os.MkdirAll(tree, 0755); err != nil {
		return fmt.Errorf("creating the build tree %s: %w", tree, err)
	}

	// Mirror, so anything the previous build left behind that the current
	// sources no longer contain is deleted — external/ included, which is
	// repopulated from the registry immediately below.
	if err := dirMirror(pristine, tree, nil); err != nil {
		return fmt.Errorf("copying the gomc sources into %s: %w", tree, err)
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
		goModTidy()
		buildServerInPlace(config.BuildTreeDir(), serverOutputPath())
		return
	}

	requirePrivilege("rebuild")

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

	staged := filepath.Join(buildStagingDir(), "gomc-server")
	_ = os.Remove(staged)

	fmt.Fprintf(os.Stderr, "Building gomc-server as %s...\n", who)
	if err := runBuildPhase(uid, gid, config.BuildTreeDir(), staged); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: building gomc-server: %v\n", err)
		os.Exit(1)
	}

	out := serverOutputPath()
	if err := installServer(staged, out, uid); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: installing gomc-server: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "gomc-server installed: %s\n", out)
}

// runBuildPhase re-execs this binary as the unprivileged build identity to do
// the compiling.
//
// Re-exec rather than an in-process setuid. Not for the old one-thread reason
// — syscall.Setuid has applied to every thread since Go 1.16 — but because a
// fresh process is also the only honest way to hand the build a clean
// environment, and it puts a process boundary exactly at the staging hand-off.
func runBuildPhase(uid, gid int, tree, out string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary to re-exec: %w", err)
	}

	cmd := exec.Command(self, buildPhaseArg, tree, buildScratchDir(), out)
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
	// A build environment stated in full rather than inherited: HOME and the
	// caches must not fall back to root's, GOTOOLCHAIN=local keeps the build
	// on the toolchain LinuxCNC was configured with, and PATH has to be able
	// to find the compiler and the linker.
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
	// $GOMC_DIR must not leak into the child: it would send the build phase
	// back to the pristine tree, silently dropping every external module.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr,
			"modcompile "+buildPhaseArg+": refusing to compile as root; "+
				"this phase exists precisely so that the compiler does not")
		os.Exit(1)
	}

	// The copy is called "gomc", and the name carries meaning: a handful of
	// headers include their way in from the source root outwards
	// ("gomc/generated/gmi/canon/canon_api.h"), which resolves against the
	// tree's parent directory only if the tree is called what those includes
	// say it is. buildServerInPlace passes that parent as an include root.
	treeCopy := filepath.Join(scratch, "gomc")
	if err := os.MkdirAll(treeCopy, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: creating the scratch tree: %v\n", err)
		os.Exit(1)
	}
	if err := dirMirror(tree, treeCopy, nil); err != nil {
		fmt.Fprintf(os.Stderr, "modcompile: copying the build tree into %s: %v\n", treeCopy, err)
		os.Exit(1)
	}

	goModTidyIn(treeCopy)
	buildServerInPlace(treeCopy, out)
}

// installServer moves the staged binary into place and reapplies the file
// capabilities the previous one carried.
//
// The staged file was produced by an identity that is not root, in a directory
// that identity owns, so it is opened with O_NOFOLLOW and checked to be a
// regular file belonging to that identity before root copies a byte of it.
func installServer(staged, out string, uid int) error {
	f, err := os.OpenFile(staged, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("opening the staged binary %s: %w", staged, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspecting the staged binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the staged binary %s is not a regular file", staged)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine the owner of %s", staged)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("the staged binary %s belongs to uid %d, expected the build identity %d",
			staged, st.Uid, uid)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(out), err)
	}

	// Capabilities are read before the replacement, and through whatever
	// symlink chain $(bindir) currently resolves to: on a system nobody has
	// rebuilt yet, `out` is a symlink onto the package-owned binary and its
	// capabilities are the ones to carry over.
	oldCaps := getFileCaps(out)

	// Write beside the target and rename, so a failure part-way through
	// leaves the running server's binary untouched.
	tmp := out + ".new"
	_ = os.Remove(tmp)
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
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
	// os.OpenFile applies the umask; the binary must be executable regardless
	// of the umask the administrator happened to run sudo under.
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("taking ownership of %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", out, err)
	}

	if oldCaps != "" {
		applyFileCaps(out, oldCaps)
	} else {
		fmt.Fprintf(os.Stderr,
			"modcompile: warning: %s carried no file capabilities, so none were applied.\n"+
				"  Realtime will not start until they are; on a packaged system they are\n"+
				"  applied by the package's postinst.\n", out)
	}
	return nil
}
