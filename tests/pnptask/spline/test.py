#!/usr/bin/env python3
"""A leg on a smoothed path (D47-D50), run by the real trajectory planner.

PATH_SHAPER = spline: the leg from the component tray's first slot (100, 380)
to process station 21, moved to (470, 320) behind the first dead zone
(340..420 x 300..370), has to pass the zone. The route turns under it on the
offset zone's 12 mm corner arcs; the path planner drives a natural spline
through the route instead, sent as a tangent chain of arcs and lines (D48).

What this checks on the commanded position, captured every servo cycle:

  - the head keeps CLEARANCE (12 mm) from both dead zones all the way;
  - it never stops between the tray and the station: every join of the chain
    is tangent, so the TP blends none of them;
  - it turns on wide radii (the spline's tightest arc is 80 mm; the route's
    blended corners would be a few mm), and no axis exceeds MOVE_ACC;
  - no axis exceeds MOVE_VEL, while the path runs faster than that on the
    diagonal arcs (each arc's speed is bounded per axis over its sweep);
  - the leg takes what pnproute's TP model predicts for it (D50): 855 ms at
    MOVE_VEL 600, MOVE_ACC 3000, [TRAJ]MAX_LINEAR_VELOCITY 800 -- the route
    would take 1044 ms. Within 3 %: on curved legs tpmod runs 2 % slower
    than the model (it enters the closing line later).

The times are counted in servo cycles (the capture's tags), so a slow,
non-realtime host gives the same numbers.
"""

import math
import os
import time

import pnpdrv

CLEARANCE = 12.0
ZONES = [(340.0, 300.0, 420.0, 370.0), (500.0, 60.0, 560.0, 140.0)]
START = (100.0, 380.0)
GOAL = (470.0, 320.0)
TRAVEL_Z = 40.0
MODEL_MS = 855.3
MOVE_ACC = 3000.0
MOVE_VEL = 600.0
CYCLE = 0.001


def zone_distance(x, y, z):
    x0, y0, x1, y1 = z
    dx = max(x0 - x, 0.0, x - x1)
    dy = max(y0 - y, 0.0, y - y1)
    if dx == 0.0 and dy == 0.0:
        return -min(x - x0, x1 - x, y - y0, y1 - y)
    return math.hypot(dx, dy)


def load_capture():
    rows = []
    with open("path.txt") as f:
        for line in f:
            fs = line.split()
            if len(fs) == 4:
                rows.append((int(fs[0]), float(fs[1]), float(fs[2]), float(fs[3])))
    return rows


m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

err = m.run_job(10, 21)
pnpdrv.check_eq(err, 0, "the tray -> station 21 job completed without an error")
pnpdrv.check(m.get("proc.21.has-material"), "station 21 holds the part")

# The capture is written by the filestream I/O thread; wait until it has
# caught up with the machine standing at the station again.
m.wait_still()
deadline = time.monotonic() + 10 * float(os.environ.get("STMAK_TEST_TIMEOUT_SCALE", "1"))
rows = load_capture()
while time.monotonic() < deadline:
    last = rows[-1] if rows else None
    if last and abs(last[1] - GOAL[0]) < 1e-6 and abs(last[2] - GOAL[1]) < 1e-6 and last[3] >= TRAVEL_Z - 1e-6:
        break
    time.sleep(0.05)
    rows = load_capture()

tags = [r[0] for r in rows]
pnpdrv.check(all(b - a == 1 for a, b in zip(tags, tags[1:])),
             "the capture lost no servo cycle")

# The leg: the last cycle standing at the tray slot at travel height, then up
# to the first cycle at the station.
at = lambda r, p: abs(r[1] - p[0]) < 1e-6 and abs(r[2] - p[1]) < 1e-6
i1 = next((i for i, r in enumerate(rows) if at(r, GOAL)), None)
pnpdrv.check(i1 is not None, "the head reached station 21")
i0 = max(i for i in range(i1) if at(rows[i], START) and abs(rows[i][3] - TRAVEL_Z) < 1e-6)
leg = rows[i0:i1 + 1]
pnpdrv.check(all(abs(r[3] - TRAVEL_Z) < 1e-6 for r in leg), "the leg runs at the movement height")

clear = min(zone_distance(r[1], r[2], z) for r in leg for z in ZONES)
pnpdrv.check(clear >= CLEARANCE - 1e-3, "the head keeps the clearance from both zones",
             lambda: "closest approach %.3f mm" % clear)

speed = [math.hypot(b[1] - a[1], b[2] - a[2]) / CYCLE for a, b in zip(leg, leg[1:])]
peak = max(speed)
inner = speed[len(speed) // 10: -len(speed) // 10]
pnpdrv.check(min(inner) > 0.25 * peak, "the head never stops on the way",
             lambda: "slowest %.1f mm/s in the middle of the leg, peak %.1f" % (min(inner), peak))

acc = 0.0
for a, b, c in zip(leg, leg[1:], leg[2:]):
    for k in (1, 2):
        acc = max(acc, abs(c[k] - 2 * b[k] + a[k]) / CYCLE ** 2)
pnpdrv.check(acc <= 1.05 * MOVE_ACC, "no axis exceeds MOVE_ACC",
             lambda: "peak axis acceleration %.0f mm/s^2" % acc)

vax = max(abs(b[k] - a[k]) / CYCLE for a, b in zip(leg, leg[1:]) for k in (1, 2))
pnpdrv.check(vax <= MOVE_VEL + 1e-3, "no axis exceeds MOVE_VEL",
             lambda: "peak axis speed %.1f mm/s" % vax)
pnpdrv.check(peak > 1.1 * MOVE_VEL, "the path runs faster than MOVE_VEL on the diagonal",
             lambda: "peak path speed %.1f mm/s" % peak)

# The tightest turn, from points about 5 mm apart along the path.
pts, along = [leg[0]], 0.0
for a, b in zip(leg, leg[1:]):
    along += math.hypot(b[1] - a[1], b[2] - a[2])
    if along >= 5.0:
        pts.append(b)
        along = 0.0
min_r = float("inf")
for a, b, c in zip(pts, pts[1:], pts[2:]):
    ab, bc, ac = (math.hypot(q[1] - p[1], q[2] - p[2]) for p, q in ((a, b), (b, c), (a, c)))
    area2 = abs((b[1] - a[1]) * (c[2] - a[2]) - (b[2] - a[2]) * (c[1] - a[1]))
    if area2 > 1e-9:
        min_r = min(min_r, ab * bc * ac / (2 * area2))
pnpdrv.check(min_r >= 60.0, "the head turns on the spline's wide radii",
             lambda: "tightest turn %.1f mm" % min_r)

moving = next(i for i in range(1, len(leg)) if not at(leg[i], START))
ms = (leg[-1][0] - leg[moving - 1][0]) * CYCLE * 1000
pnpdrv.check(abs(ms - MODEL_MS) <= 0.03 * MODEL_MS, "the leg takes the TP model's time (D50)",
             lambda: "%.0f ms, model %.0f ms" % (ms, MODEL_MS))

pnpdrv.done()
