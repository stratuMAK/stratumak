// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ngcpreview

// An o-word call into a separate file restarts the interpreter's line
// numbering, so a preview segment's line_no collides with the identically
// numbered line of every other file the program touches. These pin the file
// table and the per-segment index that tell them apart.

import (
	"path/filepath"
	"testing"
)

func TestGenPreviewAttributesSegmentsToTheirSourceFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // find_ngc_file's first branch resolves the sub against cwd

	// The sub's two feeds sit on ITS lines 2 and 3 — the same numbers as the
	// main program's first two feeds, which is exactly the collision.
	writeProg(t, dir, "mysub.ngc",
		"o<mysub> sub\ng1 y5 f100\ng1 y6\no<mysub> endsub\nm2\n")
	p := writeProg(t, dir, "main.ngc",
		"g21\ng1 x1 f100\ng1 x2\no<mysub> call\ng1 x3\nm2\n")

	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected preview error: %q", res.Error)
	}

	if len(res.Files) != 2 {
		t.Fatalf("Files = %v, want the previewed file plus the sub it calls", res.Files)
	}
	if !filepath.IsAbs(res.Files[0]) || filepath.Base(res.Files[0]) != "main.ngc" {
		t.Errorf("Files[0] = %q, want the previewed file as an absolute path", res.Files[0])
	}
	// The sub was opened by a cwd-relative name; clients fetch its text from
	// the server by this path, so it must not leave here relative.
	if !filepath.IsAbs(res.Files[1]) || filepath.Base(res.Files[1]) != "mysub.ngc" {
		t.Errorf("Files[1] = %q, want the sub-file as an absolute path", res.Files[1])
	}

	// Six feeds: main 2,3 → sub 2,3 → main 5. Same line numbers, different
	// files — the pair, not the number, identifies the location.
	type loc struct {
		line int32
		file int32
	}
	var got []loc
	for _, s := range res.Segments {
		got = append(got, loc{s.LineNo, s.FileIdx})
	}
	want := []loc{{2, 0}, {3, 0}, {2, 1}, {3, 1}, {5, 0}}
	if len(got) != len(want) {
		t.Fatalf("segments = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d at line %d of file %d, want line %d of file %d",
				i, got[i].line, got[i].file, want[i].line, want[i].file)
		}
	}
}

// max_line sizes reasoning about the file being displayed, so lines executed
// inside a longer sub-file must not inflate it past that file's end.
func TestGenPreviewMaxLineExcludesSubFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// 19 lines, with geometry on line 18 — well past main.ngc's 6.
	writeProg(t, dir, "longsub.ngc",
		"o<longsub> sub\ng1 y5 f100\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\ng1 y7\no<longsub> endsub\n")
	p := writeProg(t, dir, "main.ngc",
		"g21\ng1 x1 f100\ng1 x2\no<longsub> call\ng1 x3\nm2\n")

	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected preview error: %q", res.Error)
	}
	if res.MaxLine != 5 {
		t.Errorf("MaxLine = %d, want 5 (main.ngc's own last executed line, "+
			"not the sub's line numbering)", res.MaxLine)
	}
	// The sub's geometry is still there — it is attributed, not dropped.
	var fromSub int
	for _, s := range res.Segments {
		if s.FileIdx == 1 {
			fromSub++
		}
	}
	if fromSub != 2 {
		t.Errorf("%d segments attributed to the sub-file, want 2", fromSub)
	}
}

// A program with no calls must look exactly as it did before file tracking:
// one file, every segment index 0.
func TestGenPreviewSingleFileIsAllIndexZero(t *testing.T) {
	dir := t.TempDir()
	p := writeProg(t, dir, "plain.ngc", "g21\ng1 x1 f100\ng1 x2\nm2\n")
	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("Files = %v, want just the previewed file", res.Files)
	}
	for i, s := range res.Segments {
		if s.FileIdx != 0 {
			t.Errorf("segment %d attributed to file %d in a single-file program", i, s.FileIdx)
		}
	}
}
