#!/usr/bin/env python3
"""The alternating-picker reference flow of §8, on the real stack.

With two pickers a process station never has to stand empty: the free picker
takes the finished part out while the other one puts the next one in. The part
that came out is then homeless — its station is running its process on the piece
that replaced it — so the next job has to be the one that carries it away, and
any other origin is refused with ALT_PICKER_SEQUENCE.

Roles are not fixed (D20): which picker does which half is decided at the moment
it is needed, so this asserts *that a picker* holds the removed part rather than
naming one.
"""

import pnpdrv


def holder(machine):
    """Which pickers physically hold a part, per the simulated grippers."""
    return [n for n in (0, 1) if machine.holding(n)]


m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── 1. both pickers empty: tray -> press ─────────────────────────────────────

pnpdrv.check_eq(m.run_job(10, 20), 0, "job 10 -> 20 fills the empty press")
pnpdrv.check(m.get("proc.20.has-material"), "the press holds part A")
pnpdrv.check_eq(holder(m), [], "both pickers are empty afterwards")

# ── 2. tray -> press again: the swap ─────────────────────────────────────────

pnpdrv.check_eq(m.run_job(10, 20), 0, "job 10 -> 20 into the occupied press swaps")
s = m.snapshot()
pnpdrv.check(s["proc.20.has-material"], "the press holds part B — it never stood empty")
pnpdrv.check_eq(len(holder(m)), 1, "one picker came away with the part it took out")

pnpdrv.check_eq(m.run_job(10, 21), 19,
                "a job from anywhere else is refused with ALT_PICKER_SEQUENCE")
m.clear_error()

# ── 3. press -> test station: the pick is skipped ────────────────────────────
#
# Part A is already in a picker, so this job never goes near station 20. A
# gripper that grips nothing from here on proves it: an approach to the station
# would find the fixture "empty" and raise PROC_NO_MATERIAL instead.

m.miss(0, -1)
m.miss(1, -1)
pnpdrv.check_eq(m.run_job(20, 21), 0, "job 20 -> 21 places the held part with no pick")
m.miss(0, 0)
m.miss(1, 0)

s = m.snapshot()
pnpdrv.check(s["proc.21.has-material"], "the test station holds part A")
pnpdrv.check(s["proc.20.has-material"], "the press still holds part B")
pnpdrv.check_eq(holder(m), [], "both pickers are free again")

# ── 4. and around once more, this time swapping at the test station ──────────

pnpdrv.check_eq(m.run_job(10, 20), 0, "job 10 -> 20 swaps part B out for part C")
pnpdrv.check_eq(len(holder(m)), 1, "a picker holds part B")

pnpdrv.check_eq(m.run_job(20, 21), 0,
                "job 20 -> 21 swaps part A out of the test station and puts part B in")
s = m.snapshot()
pnpdrv.check(s["proc.21.has-material"], "the test station holds part B")
pnpdrv.check_eq(len(holder(m)), 1, "a picker holds part A, taken out of station 21")

pnpdrv.check_eq(m.run_job(10, 20), 19,
                "the obligation moved with it: only a job from 21 is accepted now")
m.clear_error()

# ── 5. out to the tray, and the world is level again ─────────────────────────

pnpdrv.check_eq(m.run_job(21, 11), 0, "job 21 -> 11 carries part A to the output tray")
s = m.snapshot()
pnpdrv.check(s["tray.11.full"], "the output tray took it")
pnpdrv.check(s["proc.21.has-material"], "the test station kept part B")
pnpdrv.check_eq(holder(m), [], "no picker is loaded any more")

pnpdrv.check_eq(m.run_job(10, 21), 0, "with no obligation standing, any job runs again")

pnpdrv.done()
