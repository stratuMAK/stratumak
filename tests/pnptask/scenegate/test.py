#!/usr/bin/env python3
"""The scene gate (D45).

Station 22 sits inside the fixture plate zones_alt.dxf draws and zones.dxf does
not: that plate is the movable region, and every leg into or out of the station
touches it. Such a leg starts only under the lock with whatever changes the
scene -- scene-hold in, scene-busy out -- and waits for it standing, between
legs. A scene change asked for while such a leg runs does not stop it; the
next one waits.
"""

import pnpdrv
import stmak_test

OPEN = 0                     # zones.dxf: the plate is not drawn, station 22 reachable

m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── a leg into the region waits while the scene is held ──────────────────────

m.set("scene-hold", True)
m.start_job(10, 22, deadzone=OPEN)
m.wait_gripper(0, True)      # the pick at the tray: that leg is clear of the plate
m.wait_still()
m.cycles(30)
s = m.snapshot()
pnpdrv.check(s["busy"] and s["start-job"], "the job waits at the gate, still running")
pnpdrv.check(not s["scene-busy"], "scene-busy stays low while the leg waits")
pnpdrv.check(not s["proc.22.has-material"], "nothing reached the station")
at = (s["picker.0.pos-x"], s["picker.0.pos-y"])
m.cycles(30)
s = m.snapshot()
pnpdrv.check_eq((s["picker.0.pos-x"], s["picker.0.pos-y"]), at,
                "the head stands still while it waits")

m.set("scene-hold", False)
m.wait(lambda s: s["scene-busy"], "scene-busy once the hold drops and the leg goes")
pnpdrv.check_eq(m.await_job(), 0, "the job ends once the scene is free")
s = m.snapshot()
pnpdrv.check(s["proc.22.has-material"], "the station holds the part")
pnpdrv.check(not s["scene-busy"], "scene-busy is down after the job")

# ── a hold raised during a gated leg: that leg runs on, the next one waits ──
#
# The head stands at the station after the first job, so the gated leg here
# is the one OUT of it: a real travel, long enough to raise the hold into.
# (The leg into a station the head already stands at is zero-length; its lock
# is held for a cycle or two, which a poll from here cannot be relied on to
# see.)

m.start_job(22, 10, deadzone=OPEN)
# Under way, not merely taking the lock: scene-busy is up for the one cycle
# in which the gate looks at scene-hold once more, and a hold raised into
# that cycle wins (the leg yields and waits). The hold here must land on a
# leg that is already moving.
stmak_test.wait_until(
    lambda: m.get("scene-busy") and not pnpdrv.raw_get("pnp.mot.motion.in-position"),
    "the leg out of the station to be under way under the lock")
m.set("scene-hold", True)
pnpdrv.check_eq(m.await_job(), 0, "the leg already under the lock runs on to its end")
s = m.snapshot()
pnpdrv.check(not s["scene-busy"], "scene-busy dropped with the leg that ended")
pnpdrv.check(not s["proc.22.has-material"], "the part was taken out of the station")

m.start_job(10, 22, deadzone=OPEN)
m.wait_gripper(0, True)      # the pick at the tray, clear of the plate
m.wait_still()
m.cycles(30)
s = m.snapshot()
pnpdrv.check(s["busy"] and not s["scene-busy"], "the next leg into the region waits at the gate")
pnpdrv.check(not s["proc.22.has-material"], "nothing reached the station under the hold")
m.set("scene-hold", False)
pnpdrv.check_eq(m.await_job(), 0, "the leg in goes once the scene is free")

# Back to the tray with the station clear, for the last scenario.
m.start_job(22, 10, deadzone=OPEN)
pnpdrv.check_eq(m.await_job(), 0, "the part comes back to the tray")

# ── a gate wait given up ─────────────────────────────────────────────────────

m.fill_tray(10, step=0)
m.set("scene-hold", True)
m.start_job(10, 22, deadzone=OPEN)
m.wait_gripper(0, True)
m.wait_still()
m.set("wait-abort", True)
pnpdrv.check_eq(m.await_job(), 21, "wait-abort ends a gate wait with WAIT_ABORTED")
m.set("wait-abort", False)
s = m.snapshot()
pnpdrv.check(not s["scene-busy"], "scene-busy low after the abort")
pnpdrv.check(s["picker.0.close"], "the picker keeps its material")
m.clear_error()
m.set("scene-hold", False)

pnpdrv.done()
