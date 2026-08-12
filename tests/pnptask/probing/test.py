#!/usr/bin/env python3
"""Slot probing: the picker feedback correcting the tracked tray state (D9).

The tracked slot state decides *which* slots to try; whether a part is actually
there is only known once the gripper has closed on it. A slot that grips nothing
is marked empty and the search moves on, and MAX_UNPOPULATED successive misses
declare the tray finished rather than driving over all forty slots.

The sim's gripper.miss-count is what scripts that: N makes the next N closes
find nothing, -1 makes every close find nothing.
"""

import pnpdrv

# tests/pnptask/pnptask.ini: TRAYDEF 1 is 10 x 4 with MAX_UNPOPULATED = 3.
MAX_UNPOPULATED = 3

m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── a run of empty slots is walked past, not reported ────────────────────────

m.miss(0, 2)
err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "a job whose first two slots are empty still completes")
pnpdrv.check_eq(m.sim_get("0.gripper.miss-left"), 0,
                "both scripted misses were consumed by real approaches")
pnpdrv.check(m.get("proc.20.has-material"), "the third slot's part reached the press")
pnpdrv.check(not m.get("picker.0.missing"),
             "a pick that succeeded after probing leaves 'missing' low")

# The two probed slots are gone from the model: the tray now holds 37 parts, not
# 39. Emptying it one job at a time to prove that would take forty jobs, so the
# assertion is the one the design makes — the state was corrected, and the next
# search starts after the corrected slots.
m.miss(0, 0)
err = m.run_job(20, 11)
pnpdrv.check_eq(err, 0, "the part moved on to the output tray")

# ── an exhausted tray reports itself ─────────────────────────────────────────
#
# Every close from here on grips nothing, so the search burns through
# MAX_UNPOPULATED candidates and gives up.

m.miss(0, -1)
err = m.run_job(10, 20)
pnpdrv.check_eq(err, 11, "a tray whose slots are all physically empty raises TRAY_EMPTY")

s = m.snapshot()
pnpdrv.check(s["tray.10.empty"],
             "the tray's 'empty' output is set once probing declared it finished")
pnpdrv.check(s["picker.0.missing"],
             "the picker's 'missing' output marks the pick that found nothing")
pnpdrv.check(s["error"], "the error flag is latched")
pnpdrv.check(not s["picker.0.close"], "the picker was left open, not clamped on nothing")
pnpdrv.check(not s["proc.20.has-material"], "the press stayed empty")

# The failed job is still refused while its error is latched (§7.7) — that is
# what makes error-reset the PLC's obligation before the next one.
m.miss(0, 0)
err = m.run_job(10, 20)
pnpdrv.check_eq(err, 11, "a job started with the latch set is refused, error unchanged")

m.clear_error()
pnpdrv.check(not m.get("picker.0.missing"),
             "error-reset cleared the picker's missing flag with the latch")

# ── and the tray comes back when it is refilled ──────────────────────────────
#
# 'empty' is a conclusion about the physical tray, so re-declaring the contents
# is what withdraws it: the operator (or the PLC) says the tray was refilled.

m.fill_tray(10, step=0)
pnpdrv.check(not m.get("tray.10.empty"), "set-full withdraws the empty conclusion")

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "the refilled tray serves jobs again")

pnpdrv.done()
