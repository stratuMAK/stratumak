#!/usr/bin/env python3
"""Lift, travel and descent as one stream (D52), run by the real trajectory planner.

Z_CORNER_RADIUS = 10 on the spline config: a job station 21 (moved to
(470, 320), Z_PICK 4) -> tray 11 ((60, 60), Z_PICK 2) lifts out of the
station, travels at MOVE_HEIGHT 40 and descends into the tray as one stream:
the straight lift up to 30, a 10 mm arc in the vertical plane of the leg's
start direction onto the travel height, the path, a 10 mm arc down, and the
straight descent -- every join tangent, so the TP blends none of them.

What this checks on the commanded position, captured every servo cycle,
from the bottom of the station to the bottom of the tray:

  - the head never stops on the way;
  - it moves sideways only above travel height - radius, and never above
    the travel height;
  - the path keeps CLEARANCE (12 mm) from both dead zones;
  - no axis exceeds its limit: X/Y MOVE_VEL 600 mm/s and MOVE_ACC 3000 mm/s²,
    Z Z_VEL 200 mm/s and Z_ACC 2000 mm/s²;
  - it is faster than the same leg with separate strokes (stop at the top of
    the lift and before the descent): 1551 ms there, measured with
    Z_CORNER_RADIUS = 0 (1500 ms with 10 mm, 1436 with 24, 1465 with 48).

The times are counted in servo cycles (the capture's tags).
"""

import math
import os
import time

import pnpdrv

CLEARANCE = 12.0
ZONES = [(340.0, 300.0, 420.0, 370.0), (500.0, 60.0, 560.0, 140.0)]
A = (470.0, 320.0, 4.0)    # station 21, bottom
B = (60.0, 60.0, 2.0)      # tray 11, bottom
TRAVEL_Z = 40.0
RADIUS = 10.0
SEPARATE_MS = 1551.0
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


def at(r, p):
    return abs(r[1] - p[0]) < 1e-6 and abs(r[2] - p[1]) < 1e-6 and abs(r[3] - p[2]) < 1e-6


m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)
pnpdrv.check_eq(m.run_job(10, 21), 0, "the tray -> station 21 job completed without an error")
pnpdrv.check_eq(m.run_job(21, 11), 0, "the station 21 -> tray 11 job completed without an error")

# The capture is written by the filestream I/O thread; wait until it has
# caught up with the machine standing up at tray 11 again.
m.wait_still()
deadline = time.monotonic() + 10 * float(os.environ.get("STMAK_TEST_TIMEOUT_SCALE", "1"))
rows = load_capture()
while time.monotonic() < deadline:
    if rows and abs(rows[-1][1] - B[0]) < 1e-6 and abs(rows[-1][2] - B[1]) < 1e-6 and rows[-1][3] >= TRAVEL_Z - 1e-6:
        break
    time.sleep(0.05)
    rows = load_capture()

tags = [r[0] for r in rows]
pnpdrv.check(all(b - a == 1 for a, b in zip(tags, tags[1:])), "the capture lost no servo cycle")

i0 = max(i for i, r in enumerate(rows) if at(r, A))
i1 = next((i for i in range(i0, len(rows)) if at(rows[i], B)), None)
pnpdrv.check(i1 is not None, "the head went from the bottom of station 21 to the bottom of tray 11")
leg = rows[i0:i1 + 1]

speed = [math.dist(a[1:], b[1:]) / CYCLE for a, b in zip(leg, leg[1:])]
peak = max(speed)
inner = speed[len(speed) // 20: -len(speed) // 20]
pnpdrv.check(min(inner) > 0.1 * peak, "the head never stops between the two strokes",
             lambda: "slowest %.1f mm/s, peak %.1f" % (min(inner), peak))

# Within a micrometre: the captured joint positions come out of motion's cubic
# interpolator, which rounds the line-to-arc join by some 1e-4 mm -- the TP
# itself runs the join tangent (the lift accelerates at the full Z_ACC, which
# a blended join would halve).
low = [r for r in leg if r[3] < TRAVEL_Z - RADIUS - 1e-6]
side = max(min(math.hypot(r[1] - A[0], r[2] - A[1]), math.hypot(r[1] - B[0], r[2] - B[1])) for r in low)
pnpdrv.check(side < 1e-3, "below travel height - radius the head only moves vertically",
             lambda: "%.4f mm off the station" % side)
top = max(r[3] for r in leg)
pnpdrv.check(top <= TRAVEL_Z + 1e-6, "the head never goes above the travel height",
             lambda: "Z up to %.4f" % top)

clear = min(zone_distance(r[1], r[2], z) for r in leg for z in ZONES)
pnpdrv.check(clear >= CLEARANCE - 1e-3, "the path keeps the clearance from both zones",
             lambda: "closest approach %.3f mm" % clear)

limits = ((1, 600.0, 3000.0), (2, 600.0, 3000.0), (3, 200.0, 2000.0))
worst = []
for k, vmax, amax in limits:
    v = max(abs(b[k] - a[k]) for a, b in zip(leg, leg[1:])) / CYCLE
    a_ = max(abs(c[k] - 2 * b[k] + a[k]) for a, b, c in zip(leg, leg[1:], leg[2:])) / CYCLE ** 2
    worst.append((v / vmax, a_ / amax, "XYZ"[k - 1], v, a_))
pnpdrv.check(all(v <= 1 + 1e-6 and a <= 1.05 for v, a, *_ in worst), "no axis exceeds its velocity or acceleration",
             lambda: "; ".join("%s %.1f mm/s %.0f mm/s^2" % (n, v, a) for _, _, n, v, a in worst))

ms = (leg[-1][0] - leg[0][0]) * CYCLE * 1000
pnpdrv.check(ms <= 0.98 * SEPARATE_MS, "the stream is faster than separate strokes",
             lambda: "%.0f ms, separate strokes %.0f ms" % (ms, SEPARATE_MS))

pnpdrv.done()
