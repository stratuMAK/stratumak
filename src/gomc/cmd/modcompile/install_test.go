// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

// Tests for the privileged install path: the refusals installStaged makes at
// the trust boundary, the setcap-before-rename ordering, the staging
// ownership decisions, the operation lock, the install-time collision
// refusal, and the guard logic of the unprivileged phases. None of these need
// root — where the real thing does (chown to 0:0), the decision is factored
// out or indirected so the decision itself is what gets tested.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sittner/linuxcnc/src/gomc/internal/config"
)

// assertNotInstalled: a refused install must leave no trace at the
// destination — neither the target nor a stranded temp file.
func assertNotInstalled(t *testing.T, out string) {
	t.Helper()
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Errorf("a refused install must not create %s (lstat error: %v)", out, err)
	}
	if _, err := os.Lstat(out + ".new"); !os.IsNotExist(err) {
		t.Errorf("a refused install must not leave the temp file %s behind (lstat error: %v)", out+".new", err)
	}
}

// TestInstallStagedRefusals covers the three ways installStaged refuses a
// staged file at the trust boundary. Each case asserts the refusal REASON:
// the staged file sits in a directory the build identity owns, so "it did not
// install" without "and it said why" would also describe a crash.
func TestInstallStagedRefusals(t *testing.T) {
	uid := os.Getuid()

	t.Run("non-regular file", func(t *testing.T) {
		dir := t.TempDir()
		staged := filepath.Join(dir, "staged")
		if err := os.Mkdir(staged, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		out := filepath.Join(dir, "install", "gomc-server")
		err := installStaged(staged, out, uid, 0o755)
		if err == nil {
			t.Fatal("a directory where the staged binary belongs must be refused")
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("the refusal must say the staged path is not a regular file; got: %v", err)
		}
		assertNotInstalled(t, out)
	})

	t.Run("wrong owner", func(t *testing.T) {
		dir := t.TempDir()
		staged := filepath.Join(dir, "staged")
		if err := os.WriteFile(staged, []byte("elf"), 0o755); err != nil {
			t.Fatalf("writing: %v", err)
		}
		out := filepath.Join(dir, "install", "gomc-server")
		// The file belongs to this test's uid; expecting a different one is
		// exactly the "somebody else put this here" case.
		err := installStaged(staged, out, uid+1, 0o755)
		if err == nil {
			t.Fatal("a staged file not owned by the build identity must be refused")
		}
		for _, want := range []string{"belongs to uid", "expected the build identity"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must state the ownership mismatch (%q); got: %v", want, err)
			}
		}
		assertNotInstalled(t, out)
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "somewhere-else")
		if err := os.WriteFile(target, []byte("elf"), 0o755); err != nil {
			t.Fatalf("writing: %v", err)
		}
		staged := filepath.Join(dir, "staged")
		if err := os.Symlink(target, staged); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		out := filepath.Join(dir, "install", "gomc-server")
		// O_NOFOLLOW makes the open itself fail: the link is never followed,
		// so there is nothing later to check and nothing to install.
		err := installStaged(staged, out, uid, 0o755)
		if err == nil {
			t.Fatal("a symlink where the staged binary belongs must be refused")
		}
		if !strings.Contains(err.Error(), "opening the staged file") {
			t.Errorf("the refusal must come from the O_NOFOLLOW open; got: %v", err)
		}
		assertNotInstalled(t, out)
	})
}

// withInstallChownStub disables the chown-to-root step so the write→prepare→
// rename ordering can be exercised without CAP_CHOWN. Only the ordering is
// under test in the callers; the ownership handling itself is untouched
// everywhere else.
func withInstallChownStub(t *testing.T) {
	t.Helper()
	old := installChown
	installChown = func(string, int, int) error { return nil }
	t.Cleanup(func() { installChown = old })
}

// TestInstallStagedPrepRunsBeforeRename pins the ordering fix 1 is about: the
// prepare hook (setcap, in production) runs on the temp file while the old
// binary still occupies the installed path, so the rename that swaps them
// carries contents and capabilities in one atomic step.
func TestInstallStagedPrepRunsBeforeRename(t *testing.T) {
	withInstallChownStub(t)
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, []byte("new server"), 0o755); err != nil {
		t.Fatalf("writing: %v", err)
	}
	out := filepath.Join(dir, "gomc-server")
	if err := os.WriteFile(out, []byte("old server"), 0o755); err != nil {
		t.Fatalf("writing: %v", err)
	}

	var prepPath, outDuringPrep string
	err := installStagedPrep(staged, out, os.Getuid(), 0o755, func(tmp string) error {
		prepPath = tmp
		b, err := os.ReadFile(out)
		if err != nil {
			t.Errorf("reading the installed path during prepare: %v", err)
		}
		outDuringPrep = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("installStagedPrep: %v", err)
	}
	if want := out + ".new"; prepPath != want {
		t.Errorf("prepare ran on %q, want the staged temp file %q", prepPath, want)
	}
	if outDuringPrep != "old server" {
		t.Errorf("at prepare time the installed path held %q — the rename must not have happened yet", outDuringPrep)
	}
	if b, _ := os.ReadFile(out); string(b) != "new server" {
		t.Errorf("after the install the target holds %q, want the new binary", b)
	}
	if _, err := os.Lstat(out + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temp file must be gone after the rename (lstat error: %v)", err)
	}
}

// TestInstallStagedPrepFailureLeavesOldBinary: a failed setcap must abort the
// install with the old binary untouched and the temp removed — the machine
// keeps its working (and capability-carrying) server.
func TestInstallStagedPrepFailureLeavesOldBinary(t *testing.T) {
	withInstallChownStub(t)
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, []byte("new server"), 0o755); err != nil {
		t.Fatalf("writing: %v", err)
	}
	out := filepath.Join(dir, "gomc-server")
	if err := os.WriteFile(out, []byte("old server"), 0o755); err != nil {
		t.Fatalf("writing: %v", err)
	}

	sentinel := errors.New("setcap: operation not permitted")
	err := installStagedPrep(staged, out, os.Getuid(), 0o755, func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("the prepare failure must abort the install with its own error; got: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "old server" {
		t.Errorf("after the aborted install the target holds %q, want the old binary untouched", b)
	}
	if _, err := os.Lstat(out + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temp file must be removed on abort (lstat error: %v)", err)
	}
}

// TestStagingStaysRootOwnedByDecision pins the ownership split fix 2 rests
// on: the cache root and the staging directories that root writes and renames
// inside must be in the root-owned set, parents before children, and never in
// the set handed to the build identity. Moving one across is how the symlink
// redirect comes back.
func TestStagingStaysRootOwnedByDecision(t *testing.T) {
	rootSet := make(map[string]bool)
	for _, d := range rootOwnedCacheDirs() {
		rootSet[d] = true
	}
	identitySet := make(map[string]bool)
	for _, d := range buildIdentityCacheDirs() {
		identitySet[d] = true
	}

	for _, d := range []string{
		buildCacheDir,
		buildStagingDir(),
		filepath.Join(buildStagingDir(), "cmod"),
	} {
		if !rootSet[d] {
			t.Errorf("%s must stay root-owned: root writes and renames inside it", d)
		}
		if identitySet[d] {
			t.Errorf("%s must not be handed to the build identity", d)
		}
	}
	for _, d := range []string{
		buildHomeDir(), buildGoCacheDir(), buildGoModDir(), buildScratchDir(),
	} {
		if !identitySet[d] {
			t.Errorf("%s belongs to the build identity, which compiles in it", d)
		}
		if rootSet[d] {
			t.Errorf("%s must not be in the root-owned set", d)
		}
	}

	// Parents strictly before children: a directory taken back after the one
	// inside it leaves a window in which the child can be renamed away.
	dirs := rootOwnedCacheDirs()
	pos := make(map[string]int, len(dirs))
	for i, d := range dirs {
		pos[d] = i
	}
	for i, d := range dirs {
		if pi, ok := pos[filepath.Dir(d)]; ok && pi >= i {
			t.Errorf("%s is taken back before its parent %s", d, filepath.Dir(d))
		}
	}
}

// TestEnsureOwnedDirNeverFollowsAPlantedLink: a symlink sitting where one of
// root's directories belongs is the attack ensureOwnedDir exists to defuse.
// It must be removed — not followed, not chmodded/chowned through — and the
// link target must come out untouched.
func TestEnsureOwnedDirNeverFollowsAPlantedLink(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	base := t.TempDir()

	victim := filepath.Join(base, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	canary := filepath.Join(victim, "canary")
	if err := os.WriteFile(canary, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	d := filepath.Join(base, "staging")
	if err := os.Symlink(victim, d); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := ensureOwnedDir(d, uid, gid); err != nil {
		t.Fatalf("ensureOwnedDir over a planted symlink: %v", err)
	}
	fi, err := os.Lstat(d)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Errorf("%s must be a real directory afterwards, got mode %v", d, fi.Mode())
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("%s has mode %o, want 0755", d, fi.Mode().Perm())
	}
	// The link target was not followed: its mode and contents are untouched.
	if vfi, err := os.Stat(victim); err != nil || vfi.Mode().Perm() != 0o700 {
		t.Errorf("the link target's mode changed (now %v, err %v) — the symlink was followed", vfi.Mode().Perm(), err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Errorf("the link target's contents changed: %v", err)
	}
}

// TestEnsureOwnedDirShapes covers the remaining decisions: a regular file is
// replaced, an existing directory is kept (contents intact) with its mode
// corrected, and a missing path is created.
func TestEnsureOwnedDirShapes(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	base := t.TempDir()

	file := filepath.Join(base, "was-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := ensureOwnedDir(file, uid, gid); err != nil {
		t.Fatalf("ensureOwnedDir over a file: %v", err)
	}
	if fi, err := os.Lstat(file); err != nil || !fi.IsDir() {
		t.Errorf("a file in the way must be replaced by a directory (err %v)", err)
	}

	keep := filepath.Join(base, "keep")
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := filepath.Join(keep, "data")
	if err := os.WriteFile(data, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := ensureOwnedDir(keep, uid, gid); err != nil {
		t.Fatalf("ensureOwnedDir over an existing directory: %v", err)
	}
	if fi, _ := os.Lstat(keep); fi.Mode().Perm() != 0o755 {
		t.Errorf("an existing directory's mode must be corrected to 0755, got %o", fi.Mode().Perm())
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("an existing directory's contents must survive: %v", err)
	}

	missing := filepath.Join(base, "missing")
	if err := ensureOwnedDir(missing, uid, gid); err != nil {
		t.Fatalf("ensureOwnedDir on a missing path: %v", err)
	}
	if fi, err := os.Lstat(missing); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Errorf("a missing path must come out a 0755 directory (err %v)", err)
	}
}

// TestRunSmokeCmdReportsTheFailingOutput: the smoke test exists to catch a
// server whose init() panics before it reaches the installed path, and its
// value is the message the binary died with — so a failure must carry the
// captured output, and say the install did not happen.
func TestRunSmokeCmdReportsTheFailingOutput(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo 'panic: duplicate module registration'; exit 2")
	err := runSmokeCmd(cmd, "/staging/server/gomc-server")
	if err == nil {
		t.Fatal("a non-zero exit must fail the smoke test")
	}
	for _, want := range []string{"smoke test", "was not installed", "panic: duplicate module registration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the smoke-test failure must carry %q; got: %v", want, err)
		}
	}

	if err := runSmokeCmd(exec.Command("/bin/sh", "-c", "echo usage; exit 0"), "x"); err != nil {
		t.Errorf("a zero exit must pass the smoke test: %v", err)
	}
}

// TestCheckCModInstallCollision: `--install` must refuse a name the package
// already ships, naming both paths — the launcher refuses ambiguous names at
// machine start, and discovering that at the next boot instead of at the
// install command is the bug this closes.
func TestCheckCModInstallCollision(t *testing.T) {
	base := t.TempDir()
	shipped := filepath.Join(base, "usr", "cmod")
	oldCmod := config.EMC2CmodDir
	t.Cleanup(func() { config.EMC2CmodDir = oldCmod })
	config.EMC2CmodDir = shipped
	withStateDir(t, filepath.Join(base, "var"))

	// Nothing shipped under the name: no collision, no refusal.
	if err := checkCModInstallCollision("mycomp"); err != nil {
		t.Errorf("a fresh name must install: %v", err)
	}

	if err := os.MkdirAll(shipped, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shippedSO := filepath.Join(shipped, "mycomp.so")
	if err := os.WriteFile(shippedSO, nil, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}

	err := checkCModInstallCollision("mycomp")
	if err == nil {
		t.Fatal("installing a name the package ships must be refused")
	}
	for _, want := range []string{shippedSO, filepath.Join(config.LocalCModDir(), "mycomp.so")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %s so it can be acted on; got: %v", want, err)
		}
	}

	// Run-in-place: one cmod directory, nothing to collide with.
	withStateDir(t, "")
	if err := checkCModInstallCollision("mycomp"); err != nil {
		t.Errorf("run-in-place has a single cmod directory and must not refuse: %v", err)
	}
}

// TestLockFileIsExclusiveAndFailFast: the second taker must fail immediately
// with a message that says what is holding things up, and a released lock
// must be acquirable again.
func TestLockFileIsExclusiveAndFailFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")

	held, err := lockFile(path)
	if err != nil {
		t.Fatalf("first acquisition: %v", err)
	}

	if _, err := lockFile(path); err == nil {
		t.Fatal("a held lock must refuse a second taker")
	} else if !strings.Contains(err.Error(), "another stratumak-build operation is running") {
		t.Errorf("the refusal must say another operation is running; got: %v", err)
	}

	if err := held.Close(); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	again, err := lockFile(path)
	if err != nil {
		t.Fatalf("re-acquisition after release: %v", err)
	}
	_ = again.Close()
}

// TestRmGomodTargetRefusesTraversal: rm-gomod runs as root and ends in
// RemoveAll, so a name resolving outside the store must be refused for that
// stated reason — and the refusal happens in resolution, before anything
// destructive can run at all.
func TestRmGomodTargetRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "modules")
	outside := filepath.Join(base, "precious")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, name := range []string{"../precious", "external/../../precious", "a/b", ".."} {
		got, err := rmGomodTarget(name, store)
		if err == nil {
			t.Errorf("rmGomodTarget(%q) = %q, want a refusal", name, got)
			continue
		}
		for _, want := range []string{"is not a module name", "outside", store} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %q must state the containment reason (%q); got: %v", name, want, err)
			}
		}
	}
	// Nothing was removed by resolving: the escape target is still there.
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("resolution must not remove anything: %v", err)
	}

	// The two accepted forms resolve to the same store entry.
	for _, name := range []string{"widget", "external/widget"} {
		got, err := rmGomodTarget(name, store)
		if err != nil {
			t.Errorf("rmGomodTarget(%q): %v", name, err)
			continue
		}
		if want := filepath.Join(store, "widget"); got != want {
			t.Errorf("rmGomodTarget(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestPhaseMustNotBeRoot pins the decision logic of the unprivileged phases'
// self-refusal: euid 0 is refused with the reason stated, anything else
// passes. (The phases themselves exit on the refusal, which is why the
// decision is a function.)
func TestPhaseMustNotBeRoot(t *testing.T) {
	err := phaseMustNotBeRoot(buildPhaseArg, 0)
	if err == nil {
		t.Fatal("euid 0 must be refused")
	}
	if !strings.Contains(err.Error(), "refusing to compile as root") {
		t.Errorf("the refusal must state its reason; got: %v", err)
	}
	if !strings.Contains(err.Error(), buildPhaseArg) {
		t.Errorf("the refusal must name the phase; got: %v", err)
	}

	if err := phaseMustNotBeRoot(buildCModPhaseArg, 1000); err != nil {
		t.Errorf("an unprivileged euid must pass: %v", err)
	}
}
