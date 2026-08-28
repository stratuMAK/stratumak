#!/usr/bin/env python3
"""One approach to the enclosed station, with the release at a fixed coordinate.

BLEND_TAIL_MARGIN comes from blend.ini so there is no doubt what was in force.
busy is driven by a servo-thread comparator on X (blend.hal), so the second leg
is dispatched at exactly x = -645 -- 80 mm into the 125 mm first leg, past the
halfway mark where tpHandleBlendArc stops asking whether two segments are
tangent. checkresult reads the captured velocity.
"""
import os, sys, time
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'pnptask'))
import pnpdrv

m = pnpdrv.Machine()
m.set("deadzone-select", 0)
m.set("tray.10.set-full", False); time.sleep(0.05)
m.set("tray.10.set-full", True);  time.sleep(0.1)
m.set("tray.10.set-full", False)
print("error-id %d, wait-stops %s" % (m.run_job(10, 20), m.get("wait-stops")))
