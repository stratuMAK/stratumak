#!/usr/bin/env python3
"""One approach to the enclosed station, with the release at a fixed coordinate.

busy is driven by a servo-thread comparator on X (blend.hal), so the second leg
is dispatched at exactly x = -645 -- 80 mm into the 125 mm first leg, past the
halfway mark where tpHandleBlendArc stops asking whether two segments are
tangent. checkresult reads the captured velocity.
"""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'pnptask'))
import pnpdrv

m = pnpdrv.Machine()
# The machine must be up before the first pin write, and the tray must report
# itself stocked before the job: a hand-made 0.1 s set-full pulse could fall
# between two ticks of the pnptask loop and leave the job an empty tray.
m.wait_ready()
m.set("deadzone-select", 0)
m.fill_tray(10)
err = m.run_job(10, 20)
print("error-id %d, wait-stops %s" % (err, m.get("wait-stops")))
# A job that failed never ran the approach checkresult measures.
sys.exit(0 if err == 0 else 1)
