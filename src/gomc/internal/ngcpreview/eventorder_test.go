// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package ngcpreview

// Segments, dwells and tool changes are recorded interleaved but leave over
// three separate lists. A client has to put them back in the order they
// happened, and line numbers cannot say what that order was — an o-word loop
// revisits a line, and a call into another file restarts numbering. `seq` is
// the recorder's single counter across all three lists.

import (
	"sort"
	"testing"
)

func TestGenPreviewSeqOrdersAllThreeLists(t *testing.T) {
	dir := t.TempDir()
	// T0 keeps this independent of a tooltable service (T1 against an empty
	// table is a legitimate interp error); M6 records the tool change either
	// way, which is what is under test.
	p := writeProg(t, dir, "order.ngc",
		"g21\ng1 x1 f100\ng4 p0.01\nt0 m6\ng1 x2\nm2\n")
	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected preview error: %q", res.Error)
	}
	if len(res.Dwells) != 1 || len(res.ToolChanges) != 1 || len(res.Segments) < 2 {
		t.Fatalf("fixture did not produce the expected event mix: %d segments, "+
			"%d dwells, %d tool changes",
			len(res.Segments), len(res.Dwells), len(res.ToolChanges))
	}

	type event struct {
		seq  int32
		kind string
		line int32
	}
	var events []event
	for _, s := range res.Segments {
		events = append(events, event{s.Seq, "segment", s.LineNo})
	}
	for _, d := range res.Dwells {
		events = append(events, event{d.Seq, "dwell", d.LineNo})
	}
	for _, tc := range res.ToolChanges {
		events = append(events, event{tc.Seq, "tool_change", tc.LineNo})
	}

	// One counter across all three lists: every seq distinct.
	seen := map[int32]string{}
	for _, e := range events {
		if prev, dup := seen[e.seq]; dup {
			t.Errorf("seq %d used by both %s and %s — the lists are not sharing "+
				"one counter", e.seq, prev, e.kind)
		}
		seen[e.seq] = e.kind
	}

	sort.Slice(events, func(i, j int) bool { return events[i].seq < events[j].seq })

	// Ordering by seq must reproduce the program exactly — kind AND line:
	// the move on line 2, the dwell on 3, the tool change on 4, the move on 5.
	// The lines are only assertable because the canon interface carries a line
	// number into DWELL and CHANGE_TOOL; before that they inherited whatever
	// moved last.
	want := []event{
		{events[0].seq, "segment", 2},
		{events[1].seq, "dwell", 3},
		{events[2].seq, "tool_change", 4},
		{events[3].seq, "segment", 5},
	}
	if len(events) != len(want) {
		t.Fatalf("recorded %v, want %v", events, want)
	}
	for i := range want {
		if events[i].kind != want[i].kind || events[i].line != want[i].line {
			t.Fatalf("in seq order got %v, want %v", events, want)
		}
	}
}

// The ordering has to survive an o-word loop, where line numbers go backwards
// — the case the old line-number walk got wrong, replaying a dwell inside a
// loop against the wrong move.
func TestGenPreviewSeqSurvivesBackwardLineNumbers(t *testing.T) {
	dir := t.TempDir()
	p := writeProg(t, dir, "loop.ngc",
		"g21\no100 repeat [2]\ng1 x1 f100\ng4 p0.01\ng1 x2\ng1 x0\no100 endrepeat\nm2\n")
	m := newTestPreview(t, dir)
	res, err := m.GenPreview(p, "", "g21")
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected preview error: %q", res.Error)
	}
	if len(res.Dwells) != 2 {
		t.Fatalf("loop ran %d times by dwell count, want 2", len(res.Dwells))
	}

	// The two dwells are the same source line, so line numbers cannot order
	// them at all — but their seqs must bracket the second iteration's moves.
	d0, d1 := res.Dwells[0].Seq, res.Dwells[1].Seq
	if d0 >= d1 {
		t.Fatalf("dwell seqs %d, %d are not in emission order", d0, d1)
	}
	var between int
	for _, s := range res.Segments {
		if s.Seq > d0 && s.Seq < d1 {
			between++
		}
	}
	if between == 0 {
		t.Errorf("no segments recorded between the two dwells (seq %d..%d) — "+
			"the loop's second-iteration moves are not ordered against them",
			d0, d1)
	}
}
