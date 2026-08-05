// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ngcpreview

// GenPreview contract tests against the real interpreter (librs274 at
// runtime, like internal/task). Pins the fixes from
// NGCPREVIEW_REVIEW_FINDINGS.md: N-1 (containment), N-2 (EXECUTE_FINISH is
// success), N-3 (INI accessor), N-4 (bounds), N-6/N-9 (partial segments +
// error line), N-11 (eval injection), plus the wire unit contract.

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stratuMAK/stratumak/src/stmak/internal/pathres"
	"github.com/stratuMAK/stratumak/src/stmak/pkg/inifile"
)

// newTestPreview builds an ngcPreview whose program allow-list is progDir,
// with no tooltable/persist services (both nil-tolerant).
func newTestPreview(t *testing.T, progDir string) *ngcPreview {
	t.Helper()
	pathres.SetDefaultForTest(t, progDir)
	return &ngcPreview{
		logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		name:        "ngcpreview-test",
		linearUnits: 1.0,
		allowedDirs: []string{progDir},
		timeout:     30 * time.Second,
	}
}

func writeProg(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// N-2: EXECUTE_FINISH (tool change, probe, M66) is success-with-flag, not a
// stop condition — the preview must continue past it with no error.
func TestGenPreviewContinuesPastToolChangeAndProbe(t *testing.T) {
	dir := t.TempDir()
	// T0 M6 keeps the fixture independent of a tooltable service (T1 with an
	// empty table is a legitimate interp error); M6 sets toolchange_flag →
	// EXECUTE_FINISH either way, which is the N-2 path under test.
	p := writeProg(t, dir, "tc.ngc",
		"g21\nt0 m6\ng1 x10 f100\ng38.2 z-5 f50\ng1 x20\nm2\n")
	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	var feeds, probes int
	for _, s := range res.Segments {
		switch s.Type {
		case 2:
			feeds++
		case 4:
			probes++
		}
	}
	if feeds < 2 || probes < 1 {
		t.Fatalf("preview truncated: %d feed segments (want >=2), %d probe (want >=1) of %d total",
			feeds, probes, len(res.Segments))
	}
	if len(res.ToolChanges) != 1 {
		t.Fatalf("tool changes recorded: %d, want 1", len(res.ToolChanges))
	}
}

// N-1: gen_preview must refuse a path outside the program allow-list, and
// the refusal must be BECAUSE of containment: the identical, parseable file
// inside the allowed dir succeeds (one-mutation rule).
func TestGenPreviewRefusesUncontainedPath(t *testing.T) {
	parent := t.TempDir()
	progDir := filepath.Join(parent, "programs")
	outside := filepath.Join(parent, "outside")
	for _, d := range []string{progDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const prog = "g21\ng1 x1 f100\nm2\n"
	escapee := writeProg(t, outside, "escape.ngc", prog)
	m := newTestPreview(t, progDir)

	res, err := m.GenPreview(escapee, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "access denied") {
		t.Fatalf("uncontained path not refused for the right reason: error=%q segments=%d",
			res.Error, len(res.Segments))
	}
	if len(res.Segments) != 0 {
		t.Fatalf("refused preview still produced %d segments", len(res.Segments))
	}

	inside := writeProg(t, progDir, "inside.ngc", prog)
	res, err = m.GenPreview(inside, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || len(res.Segments) == 0 {
		t.Fatalf("contained file should preview: error=%q segments=%d", res.Error, len(res.Segments))
	}
}

// N-6 (server half) + N-9: an interp error mid-file returns the partial
// segments AND the actual failing line number.
func TestGenPreviewErrorReturnsPartialSegmentsAndLine(t *testing.T) {
	dir := t.TempDir()
	p := writeProg(t, dir, "err.ngc",
		"g21\ng1 x1 f100\ng1 x2\ng1 x3\ng1 [garbage\nm2\n")
	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatalf("expected an error for the garbage line")
	}
	var feeds int
	for _, s := range res.Segments {
		if s.Type == 2 {
			feeds++
		}
	}
	if feeds != 3 {
		t.Fatalf("partial geometry lost: %d feed segments, want 3 (error=%q)", feeds, res.Error)
	}
	if !strings.Contains(res.Error, "line 5") {
		t.Fatalf("error line wrong: %q (garbage is on line 5)", res.Error)
	}
}

// Wire contract: segment endpoints and arc centers are ALWAYS inches (AXIS
// internal units) — G21 mm input divided by 25.4, G20 passed through.
func TestGenPreviewWireUnitsInches(t *testing.T) {
	dir := t.TempDir()
	metric := writeProg(t, dir, "mm.ngc", "g21\ng0 x25.4 y0 z0\ng2 x25.4 i12.7 j0 f100\nm2\n")
	inch := writeProg(t, dir, "in.ngc", "g20\ng0 x1 y0 z0\ng2 x1 i0.5 j0 f100\nm2\n")
	m := newTestPreview(t, dir)

	rm, err := m.GenPreview(metric, "", "g21")
	if err != nil || rm.Error != "" {
		t.Fatalf("metric preview failed: %v %q", err, rm.Error)
	}
	ri, err := m.GenPreview(inch, "", "g20")
	if err != nil || ri.Error != "" {
		t.Fatalf("inch preview failed: %v %q", err, ri.Error)
	}
	if len(rm.Segments) != len(ri.Segments) {
		t.Fatalf("segment counts differ: %d vs %d", len(rm.Segments), len(ri.Segments))
	}
	for i := range rm.Segments {
		a, b := rm.Segments[i], ri.Segments[i]
		if !near(a.End.X, b.End.X) || !near(a.End.Y, b.End.Y) {
			t.Fatalf("segment %d endpoints differ: mm-prog (%f,%f) vs inch-prog (%f,%f)",
				i, a.End.X, a.End.Y, b.End.X, b.End.Y)
		}
		if a.Type == 3 && (!near(a.CenterX, b.CenterX) || !near(a.CenterY, b.CenterY)) {
			t.Fatalf("arc %d centers differ: (%f,%f) vs (%f,%f)", i, a.CenterX, a.CenterY, b.CenterX, b.CenterY)
		}
	}
}

// N-4: an endless loop WITH motion truncates at the segment cap; an endless
// loop WITHOUT motion hits the wall-clock bound. Both must return.
func TestGenPreviewBoundedOnEndlessProgram(t *testing.T) {
	dir := t.TempDir()
	m := newTestPreview(t, dir)
	m.segLimit = 500
	m.timeout = 3 * time.Second

	motion := writeProg(t, dir, "endless-motion.ngc",
		"g21\nf100\no100 while [1]\ng1 x0\ng1 x1\no100 endwhile\nm2\n")
	start := time.Now()
	res, err := m.GenPreview(motion, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "truncated") {
		t.Fatalf("endless motion loop: want truncation error, got %q", res.Error)
	}
	if len(res.Segments) == 0 || len(res.Segments) > 500 {
		t.Fatalf("segment count outside cap: %d", len(res.Segments))
	}

	quiet := writeProg(t, dir, "endless-quiet.ngc",
		"g21\n#1=0\no100 while [1]\n#1=[#1+1]\no100 endwhile\nm2\n")
	res, err = m.GenPreview(quiet, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "time limit") {
		t.Fatalf("endless quiet loop: want time-limit error, got %q", res.Error)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("bounds did not bound: %s elapsed", elapsed)
	}
}

// N-3: with an INI accessor wired, Interp::init reads SUBROUTINE_PATH — an
// o<sub> call from there previews; without the INI it must fail. This is the
// one-mutation proof that the accessor is live.
func TestGenPreviewIniAccessorSubroutinePath(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "subs")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProg(t, subDir, "mysub.ngc", "o<mysub> sub\ng1 x5 f100\no<mysub> endsub\nm2\n")
	p := writeProg(t, dir, "caller.ngc", "g21\nf100\no<mysub> call\nm2\n")

	ini, err := inifile.ParseString("[RS274NGC]\nSUBROUTINE_PATH = " + subDir + "\n")
	if err != nil {
		t.Fatal(err)
	}

	m := newTestPreview(t, dir)
	m.ini = ini
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("with SUBROUTINE_PATH via accessor the call must preview; got %q", res.Error)
	}

	m2 := newTestPreview(t, dir)
	res, err = m2.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatalf("without INI the o<mysub> call should fail — accessor test would be vacuous")
	}
}

// N-11: expressions that escape the #1=[...] bracket context are rejected.
func TestEvalExpressionRejectsInjection(t *testing.T) {
	m := newTestPreview(t, t.TempDir())
	for _, bad := range []string{"1] g0 x10", "1]", "1\ng0 x10", "1;comment"} {
		res, err := m.EvalExpression(bad)
		if err != nil {
			t.Fatal(err)
		}
		if res.Error == "" {
			t.Fatalf("injection %q not rejected", bad)
		}
	}
	res, err := m.EvalExpression("sin[30]*2 + [1+2]")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("legit expression rejected: %q", res.Error)
	}
	if !near(res.Value, 4.0) {
		t.Fatalf("sin[30]*2+[1+2] = %f, want 4.0", res.Value)
	}
}

func near(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}
