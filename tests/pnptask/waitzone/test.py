#!/usr/bin/env python3
"""The wait position derived from a dead zone (D29).

A station whose gate opens while the portal is already on its way should not be
approached by driving to a taught park spot, stopping, and driving on. Station
22 sits inside the fixture plate zones_alt.dxf draws, and instead of WAIT_X/
WAIT_Y it names two drawings: the one whose zone it is enclosed by, and the one
that applies once the enclosure opens. The approach is planned as two legs — up
to the last point that is still clear of that zone, and from there into the
station — and the second leg is queued into the trajectory planner the moment
busy drops, without waiting for the first to finish.

So there are two outcomes, and wait-stops is what tells them apart:

  * the station is still busy when the queue runs dry: the head stands at the
    derived wait point, clear of the zone, and wait-stops counts it;
  * it clears while the first leg is still running: the second is queued behind
    it, nothing stops, and wait-stops does not move.

Clearing the station means releasing its scene too — the second leg is planned
in the drawing WAIT_CLEAR_DEADZONE names, and a station that reports done while
deadzone-select still says the enclosure is shut is a sequencing bug, not a
blocked route: WAIT_SCENE_MISMATCH.
"""

import pnpdrv

# tests/pnptask/pnptask.ini: station 22 sits at (185, 205), inside the zone
# zones_alt.dxf (deadzone-select 1) draws at (150,150)..(220,260). zones.dxf
# (deadzone-select 0) leaves it clear.
BLOCKED, CLEAR = 1, 0
ZONE = (150.0, 150.0, 220.0, 260.0)
# CLEARANCE = 12 and BLEND_TOLERANCE = 2, so the wait point stands 14 mm off the
# drawn zone. The upper bound is slack for the offset construction, which
# circumscribes the corner arcs and so pushes the boundary a hair further out
# than the clearance: what it must not be is further back than that, because
# stopping as late as it safely can is the whole point.
STANDOFF_MIN, STANDOFF_MAX = 13.5, 16.0


def zone_distance(x, y):
    """Distance from (x, y) to the drawn zone rectangle, 0 inside it."""
    x0, y0, x1, y1 = ZONE
    dx = max(x0 - x, 0.0, x - x1)
    dy = max(y0 - y, 0.0, y - y1)
    return (dx * dx + dy * dy) ** 0.5


m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── the station stays busy: a stop at the derived wait point ─────────────────

m.set_busy(22, True)
m.start_job(10, 22, deadzone=BLOCKED)

m.wait_gripper(0, True)
# The approach, not the pick: wait for the head to come up on the zone rather
# than for it to stop, because it also stands still for the Z stroke at the tray.
m.wait(lambda s: zone_distance(s["picker.0.pos-x"], s["picker.0.pos-y"]) <= STANDOFF_MAX,
       "the approach to reach the zone it may not enter")
m.wait_still()

s = m.snapshot()
d = zone_distance(s["picker.0.pos-x"], s["picker.0.pos-y"])
pnpdrv.check(STANDOFF_MIN <= d <= STANDOFF_MAX,
             "the job stopped just clear of the enclosure it may not enter",
             lambda: "it stands %.3f mm off the zone, at (%g, %g)"
                     % (d, s["picker.0.pos-x"], s["picker.0.pos-y"]))
pnpdrv.check(s["busy"] and s["start-job"], "the job is still running while it waits")
pnpdrv.check(s["picker.0.close"], "the picker holds its material through the wait")
pnpdrv.check(not s["proc.22.has-material"], "nothing was placed into the busy station")

# Nothing else moves it: the wait is a wait, not a slow approach.
m.cycles(30)
at = m.snapshot()
pnpdrv.check_eq((at["picker.0.pos-x"], at["picker.0.pos-y"]),
                (s["picker.0.pos-x"], s["picker.0.pos-y"]),
                "the machine stands still while the station is busy")

# The enclosure opens: the scene is released and the station reports done.
m.set("deadzone-select", CLEAR)
m.set_busy(22, False)
err = m.await_job()
pnpdrv.check_eq(err, 0, "the last leg drives into the once-blocked station")
pnpdrv.check(m.get("proc.22.has-material"), "the station holds the part")
pnpdrv.check_eq(m.get("wait-stops"), 1,
                "the stop at the wait position was counted")

err = m.run_job(22, 10)
pnpdrv.check_eq(err, 0, "the station is emptied back into the tray")

# ── it clears in time: no stop at all ────────────────────────────────────────
#
# busy drops while the first leg is still running, so the second is queued
# behind it and the trajectory planner blends the two. The head never comes to
# a standstill, and the counter says so.

m.set_busy(22, True)
m.start_job(10, 22, deadzone=BLOCKED)
m.wait_gripper(0, True)   # the pick is done; the gated approach has just begun

m.set("deadzone-select", CLEAR)
m.set_busy(22, False)
err = m.await_job()
pnpdrv.check_eq(err, 0, "the job that never had to wait")
pnpdrv.check(m.get("proc.22.has-material"), "the station holds the part")
pnpdrv.check_eq(m.get("wait-stops"), 1,
                "the approach did not stop, so nothing was counted")

err = m.run_job(22, 10)
pnpdrv.check_eq(err, 0, "the station is emptied back into the tray")

# ── the station clears in the wrong scene ────────────────────────────────────

m.set_busy(22, True)
m.start_job(10, 22, deadzone=BLOCKED)
m.wait_gripper(0, True)
m.wait(lambda s: zone_distance(s["picker.0.pos-x"], s["picker.0.pos-y"]) <= STANDOFF_MAX,
       "the approach to reach the zone it may not enter")
m.wait_still()

# busy drops, but deadzone-select still says the enclosure is shut.
m.set_busy(22, False)
err = m.await_job()
pnpdrv.check_eq(err, 25,
                "clearing in the wrong scene is WAIT_SCENE_MISMATCH")

s = m.snapshot()
pnpdrv.check(s["picker.0.close"], "the picker still holds the material")
pnpdrv.check(not s["proc.22.has-material"], "the head never entered the enclosure")
d = zone_distance(s["picker.0.pos-x"], s["picker.0.pos-y"])
pnpdrv.check(d >= STANDOFF_MIN,
             "it stands where it stopped, clear of the zone",
             lambda: "it is %.3f mm off the zone" % d)

pnpdrv.done()
