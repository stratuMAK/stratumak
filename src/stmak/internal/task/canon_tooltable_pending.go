// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2
package task

// Pending tool-table writes.
//
// The interpreter keeps only slot 0 of the tool table resident
// (Interp::load_tool_table); every other tool is fetched on demand by
// find_tool_index / find_tool_pocket, which cache the canon's answer into
// settings->tool_table[idx]. 2.9 instead bulk-loaded the whole table at each
// synch and never re-read it in between.
//
// That makes the READ lazy while the WRITE stays queued: SetToolTableEntry
// enqueues SetToolTableEntryCmd, which the sequencer later hands to
// io.ToolSetOffset, which is what actually reaches the store. So between the
// two, an on-demand read sees the pre-write entry and the cache-fill silently
// overwrites the interpreter's own uncommitted edit. Two consecutive G10 L1
// blocks naming different axes lose the first one's axis:
//
//	g10 l1 p1 x1
//	g10 l1 p1 y2     <- find_tool_index re-reads, X reverts to 0
//
// AUTO reads ahead without draining unless a line returns EXECUTE_FINISH, and
// G10 L1 does not, so this corrupted tool offsets on a real machine.
//
// The fix is to write through to the layer the reader actually consults: an
// entry written here is recorded as pending and served to subsequent reads
// until its store write has executed, at which point the store is current and
// the pending copy is dropped.
//
// Deliberately NOT fixed by writing the store directly at enqueue time. The
// interpreter runs far ahead of execution, so that would make a G10 in an
// aborted program permanent — 2.9 discards such an edit, because the shared
// table is only touched when the queued command executes. Tool offsets are
// persistent machine state; provisional edits must stay provisional.

import "github.com/stratuMAK/stratumak/src/stmak/generated/gmi/tooltable"

// recordPendingTool stores the entry SetToolTableEntry is about to enqueue, so
// reads between now and that command's execution see it.
//
// Built read-modify-write from the current entry, as iocontrol's
// gmi_tool_set_offset does: the canon call does not carry the tool's carousel
// pocketno or its comment, and rebuilding the entry from scratch would drop
// both.
func (c *Canon) recordPendingTool(idx, toolno int32, ox, oy, oz, oa, ob, oc, ou, ov, ow,
	diameter, frontangle, backangle float64, orientation int32,
) {
	entry := c.toolEntry(idx)
	entry.Idx = idx
	entry.Toolno = toolno
	entry.XOffset, entry.YOffset, entry.ZOffset = ox, oy, oz
	entry.AOffset, entry.BOffset, entry.COffset = oa, ob, oc
	entry.UOffset, entry.VOffset, entry.WOffset = ou, ov, ow
	entry.Diameter, entry.Frontangle, entry.Backangle = diameter, frontangle, backangle
	entry.Orientation = orientation

	c.pendingToolsMu.Lock()
	defer c.pendingToolsMu.Unlock()
	if c.pendingTools == nil {
		c.pendingTools = make(map[int32]tooltable.ToolEntry)
	}
	c.pendingTools[idx] = entry
}

// toolEntry returns the current view of a slot: the pending write if there is
// one, otherwise the store's copy. A slot that cannot be read yields a zero
// entry, which is the same thing getToolSlot reports as unreadable.
func (c *Canon) toolEntry(idx int32) tooltable.ToolEntry {
	if e, ok := c.pendingTool(idx); ok {
		return e
	}
	if pkgTTClient == nil || idx < 0 {
		return tooltable.ToolEntry{Idx: idx}
	}
	entry, err := pkgTTClient.GetTool(idx)
	if err != nil {
		return tooltable.ToolEntry{Idx: idx}
	}
	return entry
}

// pendingTool returns the pending write for a slot, if any.
func (c *Canon) pendingTool(idx int32) (tooltable.ToolEntry, bool) {
	c.pendingToolsMu.Lock()
	defer c.pendingToolsMu.Unlock()
	e, ok := c.pendingTools[idx]
	return e, ok
}

// clearPendingTool drops a slot's pending write once the store holds it.
// Called from SetToolTableEntryCmd.PostWait, i.e. after io.ToolSetOffset has
// completed.
func (c *Canon) clearPendingTool(idx int32) {
	c.pendingToolsMu.Lock()
	defer c.pendingToolsMu.Unlock()
	delete(c.pendingTools, idx)
}

// clearPendingTools drops every pending write. Called from InitCanon: a reset
// or a new program abandons whatever the previous run had queued, exactly as
// the store does when the queued commands never execute.
func (c *Canon) clearPendingTools() {
	c.pendingToolsMu.Lock()
	defer c.pendingToolsMu.Unlock()
	c.pendingTools = nil
}
