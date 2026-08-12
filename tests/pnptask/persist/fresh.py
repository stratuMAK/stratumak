#!/usr/bin/env python3
"""Persistence, part 3: the same config with the state wiped.

Without this the restore assertions would pass on a machine that simply comes up
that way — every one of them is checked here against the opposite value.
"""

import pnpdrv

m = pnpdrv.Machine()

s = m.snapshot()
pnpdrv.check(not s["proc.20.has-material"], "a wiped machine's press is empty")
pnpdrv.check(not s["tray.11.full"], "its output tray is empty")
pnpdrv.check(s["tray.10.empty"], "its component tray is empty")
pnpdrv.check(not s["picker.0.close"] and not s["picker.1.close"],
             "no picker claims to hold anything")

pnpdrv.done()
