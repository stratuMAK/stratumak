// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

import (
	"path/filepath"
	"testing"
)

// An o-word call into a separate file restarts the interpreter's line
// numbering, so motion_line alone does not say where the machine is. These
// pin motion_file — the field a UI must check before it highlights a line.

func TestBuildStatReportsExecutingFile(t *testing.T) {
	task, ms := newRichTestTask()
	bringUp(t, task)

	// Segment 7 is the one motion echoes back (richMockStatus Id: 7).
	task.registerMotion(7, "/programs/subs/drill.ngc", 18)
	ms.status.Id = 7

	stat := task.BuildStat()
	if got := stat.Task.MotionLine; got != 18 {
		t.Errorf("Task.MotionLine = %d, want 18", got)
	}
	if got := stat.Task.MotionFile; got != "/programs/subs/drill.ngc" {
		t.Errorf("Task.MotionFile = %q, want the sub-file the segment came from", got)
	}
	// The loaded program is unchanged by the excursion: a UI compares the two
	// to decide whether the line it is about to highlight is even its own.
	if stat.Task.File == stat.Task.MotionFile {
		t.Errorf("Task.File tracked the sub-file (%q); it must stay the loaded program",
			stat.Task.File)
	}
}

func TestBuildStatMotionFileEmptyWhenNothingTagged(t *testing.T) {
	task, ms := newRichTestTask()
	bringUp(t, task)

	// Nothing registered for this id: idle, or the window before a
	// just-queued move's tag lands.
	ms.status.Id = 4242

	stat := task.BuildStat()
	if got := stat.Task.MotionFile; got != "" {
		t.Errorf("Task.MotionFile = %q for an untagged segment, want empty", got)
	}
	if got := stat.Task.MotionLine; got != 0 {
		t.Errorf("Task.MotionLine = %d for an untagged segment, want 0", got)
	}
}

// A relative file name reaches motion_file whenever find_ngc_file's first
// branch opened the sub by a cwd-relative path. Clients resolve motion_file
// against the server to fetch its text, so it must leave here absolute.
func TestCanonAbsolutisesRelativeFileNames(t *testing.T) {
	task, _ := newRichTestTask()
	c := task.canon

	c.task.interp = &fakeFileInterp{name: "subs/drill.ngc"}
	id := c.allocSerial(18)

	info, ok := task.motionInfoAndPrune(id)
	if !ok {
		t.Fatal("segment not registered")
	}
	if !filepath.IsAbs(info.File) {
		t.Errorf("registered file %q is not absolute", info.File)
	}
	if filepath.Base(info.File) != "drill.ngc" {
		t.Errorf("registered file %q lost the name it was opened under", info.File)
	}

	// An already-absolute name must survive verbatim: a UI compares it for
	// equality against the loaded program's path.
	c.task.interp = &fakeFileInterp{name: "/programs/main.ngc"}
	id = c.allocSerial(3)
	info, _ = task.motionInfoAndPrune(id)
	if info.File != "/programs/main.ngc" {
		t.Errorf("absolute file rewritten to %q", info.File)
	}
}

// The per-segment lookup must follow the interpreter when it changes files
// mid-execute — an MDI `o<sub> call` runs an entire sub inside one Execute,
// so a per-line snapshot would attribute all of it to the wrong file.
func TestCanonFileTracksMidExecuteSwitch(t *testing.T) {
	task, _ := newRichTestTask()
	c := task.canon

	fi := &fakeFileInterp{name: "/programs/main.ngc"}
	c.task.interp = fi
	mainID := c.allocSerial(4)
	fi.name = "/programs/subs/drill.ngc" // the call switched files
	subID := c.allocSerial(2)

	sub, _ := task.motionInfoAndPrune(subID)
	if sub.File != "/programs/subs/drill.ngc" {
		t.Errorf("segment queued after the file switch reports %q", sub.File)
	}
	// Prune drops entries below the queried id, so re-register to check the
	// earlier one independently.
	c.task.interp = &fakeFileInterp{name: "/programs/main.ngc"}
	mainID = c.allocSerial(4)
	m, _ := task.motionInfoAndPrune(mainID)
	if m.File != "/programs/main.ngc" {
		t.Errorf("segment queued before the switch reports %q", m.File)
	}
	_ = mainID
}

// The naive-CAM chain flushes while a LATER line is executing. If that later
// line was an o-word call into another file, asking the interpreter for the
// file AT FLUSH TIME files the merged move under the sub-file — with the
// line number of the main program, which in the sub means somewhere else
// entirely. Caught on a live controller: every move at a call boundary came
// back attributed to the wrong side of it.
func TestNaivecamMergedMoveKeepsItsOwnFile(t *testing.T) {
	task, mot := newNaivecamTask(t)
	c := task.canon
	enableNaivecam(c, 1.0)

	fi := &fakeFileInterp{name: "/programs/main.ngc"}
	task.interp = fi

	c.StraightFeed(3, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	c.StraightFeed(4, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	// The o-word call on the next line switches the interpreter's file, and
	// only then does something flush the open chain.
	fi.name = "/programs/subs/mysub.ngc"
	c.Dwell(5, 0)

	_, ids, _ := collectEvents(t, task, mot)
	if len(ids) != 1 {
		t.Fatalf("expected 1 merged move, got ids %v", ids)
	}
	info, ok := task.motionInfoAndPrune(ids[0])
	if !ok {
		t.Fatalf("no motionMap entry for id %d", ids[0])
	}
	if info.LineNo != 4 {
		t.Errorf("merged move line = %d, want 4 (the last chained point's)", info.LineNo)
	}
	if info.File != "/programs/main.ngc" {
		t.Errorf("merged move file = %q, want /programs/main.ngc — line 4 is the "+
			"MAIN program's, so filing it under the sub-file points at the wrong line",
			info.File)
	}
}

// fakeFileInterp answers FileName() and nothing else; allocSerial touches no
// other part of the interpreter.
type fakeFileInterp struct {
	Interpreter
	name string
}

func (f *fakeFileInterp) FileName() string { return f.name }
