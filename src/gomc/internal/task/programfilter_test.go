// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sittner/linuxcnc/src/gomc/internal/pathres"
)

// A [FILTER] program converts a source file into the G-code the interpreter
// runs. It can take minutes, so it must not block the command — and while it
// runs, the previously loaded program has to stay open and runnable.

// filterTask returns a task whose INI declares a filter for .tst, plus the
// directory the test's files live in.
func filterTask(t *testing.T, script string) (*Task, string) {
	t.Helper()
	dir := t.TempDir()
	conv := filepath.Join(dir, "conv.sh")
	if err := os.WriteFile(conv, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	pathres.SetDefaultForTest(t, dir)
	task, _ := newRichTestTask()
	bringUp(t, task)
	task.iniGet = func(section, key string) string {
		if section == "FILTER" && key == "tst" {
			return conv
		}
		if section == "FILTER" && key == "FILTER_TIMEOUT" {
			return "20"
		}
		return ""
	}
	task.programRes = pathres.ProgramResolver(func(section, key string) string {
		if section == "DISPLAY" && key == "PROGRAM_PREFIX" {
			return dir
		}
		return ""
	}, dir)
	t.Cleanup(func() {
		task.cancelFiltering()
		_ = os.RemoveAll(pathres.FilteredDir())
	})
	return task, dir
}

func writeSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFilterDone(t *testing.T, task *Task) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		task.mu.Lock()
		filtering := task.filtering
		task.mu.Unlock()
		if !filtering {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("filter never finished")
}

func TestProgramOpenFiltersWithoutBlocking(t *testing.T) {
	task, dir := filterTask(t, "sleep 0.4; echo 'FILTER_PROGRESS=50' >&2; echo 'G21'; echo 'M2'\n")
	src := writeSource(t, dir, "shape.tst", "not gcode\n")

	start := time.Now()
	if err := task.ProgramOpen(src); err != nil {
		t.Fatalf("ProgramOpen: %v", err)
	}
	// The command returns while the conversion is still running: a real
	// filter takes minutes, and every client would be frozen for it.
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("ProgramOpen blocked for %v; it must return while filtering", elapsed)
	}
	stat := task.BuildStat()
	if !stat.Task.Filtering {
		t.Error("stat does not report that a filter is running")
	}

	waitFilterDone(t, task)
	stat = task.BuildStat()
	if stat.Task.Filtering {
		t.Error("still filtering after completion")
	}
	if stat.Task.SourceFile != pathres.Canonical(src) {
		t.Errorf("source_file = %q, want the file the operator opened (%q)",
			stat.Task.SourceFile, src)
	}
	if stat.Task.File == stat.Task.SourceFile {
		t.Fatalf("file = source_file = %q; the interpreter must get the FILTERED output",
			stat.Task.File)
	}
	body, err := os.ReadFile(stat.Task.File)
	if err != nil {
		t.Fatalf("filtered program not readable: %v", err)
	}
	if string(body) != "G21\nM2\n" {
		t.Errorf("filtered program = %q", body)
	}
	// It has to live somewhere get_file and the preview are allowed to read:
	// in this instance's own directory under the shared filtered root.
	if filepath.Dir(stat.Task.File) != task.filteredDirOrDefault() {
		t.Errorf("filtered output at %q, want it under %q so the preview may read it",
			stat.Task.File, task.filteredDirOrDefault())
	}
}

// A filter that fails must leave the controller exactly as it was — not with
// no program, and never with half-converted output posing as one.
func TestFilterFailureKeepsPreviousProgram(t *testing.T) {
	task, dir := filterTask(t, "echo 'G21'; echo 'bad magic' >&2; exit 3\n")
	good := writeSource(t, dir, "good.ngc", "G21\nM2\n")
	if err := task.ProgramOpen(good); err != nil {
		t.Fatalf("ProgramOpen(good): %v", err)
	}
	loaded := task.BuildStat().Task.File

	bad := writeSource(t, dir, "broken.tst", "junk\n")
	if err := task.ProgramOpen(bad); err != nil {
		t.Fatalf("ProgramOpen(bad) rejected up front: %v", err)
	}
	waitFilterDone(t, task)

	stat := task.BuildStat()
	if stat.Task.File != loaded {
		t.Errorf("loaded program became %q after a failed filter, want %q unchanged",
			stat.Task.File, loaded)
	}
	if stat.Task.Filtering {
		t.Error("still reporting filtering after a failure")
	}
	// The filter's own words are the only explanation the operator gets.
	msgs := task.messageListSnapshot()
	var found bool
	for _, m := range msgs {
		if contains(m.Text, "bad magic") {
			found = true
		}
	}
	if !found {
		t.Errorf("the filter's stderr never reached the operator; messages: %v", msgs)
	}
	if _, err := os.Stat(filepath.Join(task.filteredDirOrDefault(), "broken.ngc")); err == nil {
		t.Error("half-converted output left behind where the preview could open it")
	}
}

// The previously loaded program must survive a failed conversion AS A FILE,
// not merely as a stat string: the ESTOP-recovery re-open and the preview's
// get_file both read it back from disk. The failing source here shares the
// previous program's base name, so this also pins the rename-on-success
// scheme — writing the new conversion straight to its final name would
// destroy its predecessor before the filter has succeeded.
func TestFilterFailureKeepsPreviousFilteredOutput(t *testing.T) {
	task, dir := filterTask(t,
		"if grep -q bad \"$1\"; then echo 'no good' >&2; exit 3; fi\n"+
			"echo 'G21'; echo 'M2'\n")
	first := writeSource(t, dir, "part.tst", "ok\n")
	if err := task.ProgramOpen(first); err != nil {
		t.Fatalf("ProgramOpen(first): %v", err)
	}
	waitFilterDone(t, task)
	loaded := task.BuildStat().Task.File
	want, err := os.ReadFile(loaded)
	if err != nil {
		t.Fatalf("first conversion unreadable: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := writeSource(t, filepath.Join(dir, "alt"), "part.tst", "bad\n")
	if err := task.ProgramOpen(bad); err != nil {
		t.Fatalf("ProgramOpen(bad) rejected up front: %v", err)
	}
	waitFilterDone(t, task)

	stat := task.BuildStat()
	if stat.Task.File != loaded {
		t.Errorf("loaded program became %q after a failed filter, want %q unchanged",
			stat.Task.File, loaded)
	}
	got, err := os.ReadFile(loaded)
	if err != nil {
		t.Fatalf("the failed conversion destroyed the loaded program's file: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("loaded program's content changed: %q -> %q", want, got)
	}
}

// Two task instances in one process must not touch each other's filtered
// output: each converts into its own directory, and neither an open nor a
// sweep in one may unlink what the other has loaded.
func TestFilteredOutputIsolatedPerInstance(t *testing.T) {
	task1, dir1 := filterTask(t, "echo 'G21'; echo 'M2'\n")
	task2, dir2 := filterTask(t, "echo 'G20'; echo 'M2'\n")
	task1.filteredDir = pathres.FilteredInstanceDir("mill1")
	task2.filteredDir = pathres.FilteredInstanceDir("mill2")

	src1 := writeSource(t, dir1, "shape.tst", "one\n")
	src2 := writeSource(t, dir2, "shape.tst", "two\n")
	if err := task1.ProgramOpen(src1); err != nil {
		t.Fatalf("task1 open: %v", err)
	}
	waitFilterDone(t, task1)
	file1 := task1.BuildStat().Task.File

	// task2 converts a SAME-NAMED source; with a shared directory this open
	// would sweep task1's loaded program away.
	if err := task2.ProgramOpen(src2); err != nil {
		t.Fatalf("task2 open: %v", err)
	}
	waitFilterDone(t, task2)
	file2 := task2.BuildStat().Task.File

	if file1 == file2 {
		t.Fatalf("both instances published to the same path %q", file1)
	}
	b1, err := os.ReadFile(file1)
	if err != nil {
		t.Fatalf("task2's open destroyed task1's loaded program: %v", err)
	}
	if string(b1) != "G21\nM2\n" {
		t.Errorf("task1's program content = %q, want its own conversion", b1)
	}
	if b2, err := os.ReadFile(file2); err != nil || string(b2) != "G20\nM2\n" {
		t.Errorf("task2's program = %q, %v", b2, err)
	}
}

// Abort means everything in progress stops, and a filter can run for minutes.
func TestAbortCancelsFiltering(t *testing.T) {
	task, dir := filterTask(t, "sleep 30; echo 'M2'\n")
	src := writeSource(t, dir, "slow.tst", "junk\n")
	if err := task.ProgramOpen(src); err != nil {
		t.Fatalf("ProgramOpen: %v", err)
	}
	task.mu.Lock()
	filtering := task.filtering
	task.mu.Unlock()
	if !filtering {
		t.Fatal("filter did not start")
	}

	start := time.Now()
	task.signalAbort()
	if stat := task.BuildStat(); stat.Task.Filtering {
		t.Error("abort left the status reporting a running filter")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("abort took %v to stop the filter", elapsed)
	}
	// Nothing may be published afterwards: the operator asked for it to stop.
	time.Sleep(300 * time.Millisecond)
	if stat := task.BuildStat(); contains(stat.Task.SourceFile, "slow.tst") {
		t.Error("a cancelled filter still published its program")
	}
}

// A plain (unfiltered) open issued while a filter is in flight supersedes the
// conversion: its result must never be published over the program the
// operator asked for afterwards. The filter script here is fast enough to
// finish inside the observation window, so if neither the cancellation nor
// the generation check worked, the stale publish WOULD happen and the test
// fails — do not slow the script down.
func TestPlainOpenSupersedesFiltering(t *testing.T) {
	task, dir := filterTask(t, "sleep 0.3; echo 'M2'\n")
	src := writeSource(t, dir, "slow.tst", "junk\n")
	if err := task.ProgramOpen(src); err != nil {
		t.Fatalf("ProgramOpen(filtered): %v", err)
	}
	task.mu.Lock()
	filtering := task.filtering
	task.mu.Unlock()
	if !filtering {
		t.Fatal("filter did not start")
	}

	plain := writeSource(t, dir, "plain.ngc", "G21\nM2\n")
	if err := task.ProgramOpen(plain); err != nil {
		t.Fatalf("ProgramOpen(plain) while filtering: %v", err)
	}
	stat := task.BuildStat()
	if stat.Task.Filtering {
		t.Error("the plain open left the superseded filter running in the status")
	}
	if stat.Task.File != pathres.Canonical(plain) {
		t.Fatalf("file = %q, want the plain program %q", stat.Task.File, plain)
	}

	// Give the superseded conversion time to reach the point where it would
	// have published its result.
	time.Sleep(800 * time.Millisecond)
	stat = task.BuildStat()
	if stat.Task.File != pathres.Canonical(plain) {
		t.Errorf("file became %q after the superseded filter finished, want %q",
			stat.Task.File, plain)
	}
	if contains(stat.Task.SourceFile, "slow.tst") {
		t.Error("the superseded filter still published its source_file")
	}
}

func TestSecondOpenWhileFilteringRejected(t *testing.T) {
	task, dir := filterTask(t, "sleep 2; echo 'M2'\n")
	first := writeSource(t, dir, "one.tst", "junk\n")
	second := writeSource(t, dir, "two.tst", "junk\n")

	if err := task.ProgramOpen(first); err != nil {
		t.Fatalf("ProgramOpen(first): %v", err)
	}
	if err := task.ProgramOpen(second); err == nil {
		t.Error("a second open was accepted while the first was still filtering; " +
			"two filters would race to publish a program")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
