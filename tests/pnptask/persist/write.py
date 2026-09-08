#!/usr/bin/env python3
"""Persistence, part 1: put the machine into a state worth remembering.

Leaves behind all three record kinds §7.2 persists — tray slot states, a process
station's occupancy, and a picker's held material — so the restart in part 2 has
something of each to restore.
"""

import pnpdrv

m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

pnpdrv.check_eq(m.run_job(10, 20), 0, "a part reached the press")
pnpdrv.check_eq(m.run_job(10, 11), 0, "a second part reached the output tray")

# A third job aborted in a busy-wait leaves its material in a picker, which is
# the held-material record — the one thing here that is not a station's state.
m.set_busy(21, True)
m.start_job(10, 21)
m.wait(lambda s: s["picker.0.close"] or s["picker.1.close"],
       "a picker to take the material")
m.set("auto-enable", False)
pnpdrv.check_eq(m.await_job(), 21, "the third job was aborted holding its material")
m.set("auto-enable", True)

s = m.snapshot()
pnpdrv.check(s["proc.20.has-material"], "the press holds a part")
pnpdrv.check(s["tray.11.full"], "the output tray is full")
pnpdrv.check(not s["tray.10.full"] and not s["tray.10.empty"],
             "the component tray is part used")
pnpdrv.check(s["picker.0.close"] or s["picker.1.close"], "a picker holds material")

pnpdrv.done()
