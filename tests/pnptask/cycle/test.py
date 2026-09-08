#!/usr/bin/env python3
"""A whole job, end to end, against the real stack.

This is the scenario the other eight are variations of: motmod and the TP in
realtime, homemod homing the joints, real HAL wiring to a simulated gripper and
fixture, and pnptask driven the only way it can be driven — over its pins.

It also carries D13's latency assertion. plan-time publishes the slowest route
plan of the job just finished (§5.2), so the budget is checked against the
planning the job actually did, not against a benchmark of the planner.
"""

import pnpdrv

# The budget of D13: a job's planning must not add more than 100 ms to its cycle
# time. The planner builds its visibility graph once at load, so what is timed
# here is the two-node insertion plus the Dijkstra.
PLAN_BUDGET = 0.1

m = pnpdrv.Machine()
m.reset_sim()

# ── the machine comes up ─────────────────────────────────────────────────────

s = m.snapshot()
pnpdrv.check(s["machine-is-on"], "the machine enabled itself from the held machine-on")
pnpdrv.check_eq(s["error-id"], 0, "no error is latched at startup")
pnpdrv.check(not s["homed"], "the joints start unhomed")
pnpdrv.check(not s["busy"], "the module starts idle")

# A tray whose geometry is known but whose slots are all -1: empty, with room.
pnpdrv.check(s["tray.10.empty"] and not s["tray.10.full"],
             "the component tray starts empty with free slots")

# ── tell it what the trays hold, then run the job ────────────────────────────

m.fill_tray(10, step=0)
pnpdrv.check(m.get("tray.10.full"), "set-full filled every slot of the component tray")

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "the tray -> press job completed without an error")

s = m.snapshot()
pnpdrv.check(s["homed"], "the first job homed the machine on its own (AUTOHOME = 1)")
pnpdrv.check(s["proc.20.has-material"], "the press holds the part")
pnpdrv.check(not s["tray.10.full"], "the component tray gave up a slot")
pnpdrv.check(not s["busy"] and not s["start-job"], "the handshake completed")
pnpdrv.check(not s["error"], "the error flag stayed low")

# The picker let go of the part and is back at movement height with nothing in
# it — the sim gripper is the physical truth here, the held record is the model.
pnpdrv.check(not m.holding(0), "picker 0 is no longer holding anything")
pnpdrv.check(not s["picker.0.close"], "picker 0's close output is low again")

plan = s["plan-time"]
pnpdrv.check(plan > 0, "plan-time reported the job's planning latency",
             lambda: "plan-time is %r" % plan)
pnpdrv.check(plan < PLAN_BUDGET,
             "planning stayed inside the %g ms budget (D13)" % (PLAN_BUDGET * 1000),
             lambda: "slowest plan took %.1f ms" % (plan * 1000))

# ── and back out again ───────────────────────────────────────────────────────

err = m.run_job(20, 11)
pnpdrv.check_eq(err, 0, "the press -> output tray job completed without an error")

s = m.snapshot()
pnpdrv.check(not s["proc.20.has-material"], "the press is empty again")
pnpdrv.check(s["tray.11.full"], "the single-position output tray took the part")
pnpdrv.check(s["plan-time"] > 0, "plan-time was re-measured for the second job")

# A job into the now-full output tray is refused before the machine moves
# (§7.4 VALIDATE), which is what makes the tray pins worth publishing.
err = m.run_job(10, 11)
pnpdrv.check_eq(err, 12, "a job into a full tray is refused with TRAY_FULL")

pnpdrv.done()
