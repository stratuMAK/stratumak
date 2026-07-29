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
		os.RemoveAll(pathres.FilteredDir())
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
	// It has to live somewhere get_file and the preview are allowed to read.
	if filepath.Dir(stat.Task.File) != pathres.FilteredDir() {
		t.Errorf("filtered output at %q, want it under %q so the preview may read it",
			stat.Task.File, pathres.FilteredDir())
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
	if _, err := os.Stat(filepath.Join(pathres.FilteredDir(), "broken.ngc")); err == nil {
		t.Error("half-converted output left behind where the preview could open it")
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
