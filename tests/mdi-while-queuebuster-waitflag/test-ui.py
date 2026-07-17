#!/usr/bin/env python3

import linuxcnc
import gmi
import gmi.constants as _gk
for _n in dir(_gk):
    if not _n.startswith('_'):
        setattr(linuxcnc, _n, getattr(_gk, _n))
import gomc_test
import sys

# Initialization
# gomc_test.Command, not gmi.Command: its wait_complete() raises on a timed-out
# wait instead of returning -1 in a 200 body, so it cannot fail silently.
c = gomc_test.Command()
s = gmi.Stat()
c.state(linuxcnc.STATE_ESTOP_RESET)
c.state(linuxcnc.STATE_ON)
c.mode(linuxcnc.MODE_MDI)

c.mdi('(print,pre 1)')
c.mdi('(print,pre 2)')
c.mdi('M400')
c.mdi('(print,post 1)')
c.mdi('(print,post 2)')
c.mdi('(print,post 3)')
c.mdi('(print,post 4)')
c.mdi('(print,post 5)')
c.mdi('(print,post 6)')

# Shutdown
c.wait_complete()
sys.exit(0)

