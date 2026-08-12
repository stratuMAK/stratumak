#!/usr/bin/env python3
"""Busy gating: waiting out an occupied process station (D15, §7.4).

A job whose destination is busy does not hover over the station — it waits at
the station's wait position, holding its material, until busy clears. Dropping
auto-enable aborts that wait (WAIT_ABORTED): the operator is asking for the
machine, and a job parked over a station forever is not a handover. The material
stays in the picker for exactly the manual handling that follows.
"""

import pnpdrv

# tests/pnptask/pnptask.ini, [PNPTASK_PROC_0]: station 20 sits at (300, 200) and
# waits out its busy at (250, 120).
WAIT_X, WAIT_Y = 250.0, 120.0

m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── the wait, and its release ────────────────────────────────────────────────

m.set_busy(20, True)
m.start_job(10, 20)

m.wait_position(0, WAIT_X, WAIT_Y)
m.wait_still()

s = m.snapshot()
pnpdrv.check(abs(s["picker.0.pos-x"] - WAIT_X) < 0.5
             and abs(s["picker.0.pos-y"] - WAIT_Y) < 0.5,
             "the job parked at the station's wait position",
             lambda: "it is at (%g, %g)" % (s["picker.0.pos-x"], s["picker.0.pos-y"]))
pnpdrv.check(s["busy"] and s["start-job"], "the job is still running while it waits")
pnpdrv.check(s["picker.0.close"], "the picker holds its material through the wait")
pnpdrv.check(m.holding(0), "the gripper really has the part")
pnpdrv.check(not s["proc.20.has-material"], "nothing was placed into the busy station")

# Nothing else moves it: the wait is a wait, not a slow approach.
m.cycles(30)
at = m.snapshot()
pnpdrv.check_eq((at["picker.0.pos-x"], at["picker.0.pos-y"]),
                (s["picker.0.pos-x"], s["picker.0.pos-y"]),
                "the machine stands still while the station is busy")

m.set_busy(20, False)
err = m.await_job()
pnpdrv.check_eq(err, 0, "the job finishes once busy clears")
pnpdrv.check(m.get("proc.20.has-material"), "the press holds the part")

err = m.run_job(20, 11)
pnpdrv.check_eq(err, 0, "the press is emptied again")

# ── the abort ────────────────────────────────────────────────────────────────

m.set_busy(20, True)
m.start_job(10, 20)
m.wait_position(0, WAIT_X, WAIT_Y)

m.set("auto-enable", False)
err = m.await_job()
pnpdrv.check_eq(err, 21, "auto-enable going low aborts the wait with WAIT_ABORTED")

s = m.snapshot()
pnpdrv.check(s["picker.0.close"], "the picker keeps holding the material for manual handling")
pnpdrv.check(m.holding(0), "the part is still physically gripped")
pnpdrv.check(not s["proc.20.has-material"], "the station never received it")

# ── and the recovery ─────────────────────────────────────────────────────────
#
# The material is in a picker and recorded against the origin, so re-commanding
# the same job completes it with no pick at all (§8's first rule, which a
# single-picker machine reaches too). A gripper that grips nothing from here on
# is what proves the pick really was skipped: a job that approached the tray
# again would walk it to TRAY_EMPTY instead of finishing.

m.set("auto-enable", True)
m.clear_error()
m.set_busy(20, False)
m.miss(0, -1)

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "re-commanding the job places the held material")
pnpdrv.check(m.get("proc.20.has-material"), "the press holds it, with no second pick")
pnpdrv.check_eq(m.sim_get("0.gripper.miss-left"), -1,
                "no close happened at the tray — the pick was skipped")

pnpdrv.done()
