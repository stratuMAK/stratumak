#!/usr/bin/env python3
"""Persistence, part 2: the same machine, restarted on the state it left.

Everything asserted here was written by write.py into ../db and read back by a
brand-new process: a fresh HAL component, fresh pins, a fresh sim. What makes
the held-material record more than a database row is the last check — the module
re-drove the picker's close output, confirmed against the gripper that the part
really is still there, and the next job skips its pick because of it (§7.6).
"""

import pnpdrv

m = pnpdrv.Machine()

s = m.snapshot()
pnpdrv.check(s["proc.20.has-material"], "the press's occupancy came back")
pnpdrv.check(s["tray.11.full"], "the output tray's slot state came back")
pnpdrv.check(not s["tray.10.full"] and not s["tray.10.empty"],
             "the component tray came back part used")

held = [n for n in (0, 1) if s["picker.%d.close" % n]]
pnpdrv.check_eq(len(held), 1, "one picker's held-material record came back")
pnpdrv.check(m.holding(held[0]),
             "the restored close output found the part still in the gripper")

# The probing state is deliberately not persisted (§7.6): it is a conclusion
# drawn from physical feedback, and re-deriving it costs one probing pass.
pnpdrv.check(not s["tray.10.empty"], "the tray is not declared empty by a stale conclusion")

# The record names the station the material came from, so the job that carries
# it away skips its pick. Grippers that grip nothing prove no pick happened.
m.reset_sim()
m.miss(0, -1)
m.miss(1, -1)
pnpdrv.check_eq(m.run_job(10, 21), 0, "the restored material is placed without a pick")
pnpdrv.check(m.get("proc.21.has-material"), "the test station received it")
pnpdrv.check_eq(m.sim_get("%d.gripper.miss-left" % held[0]), -1,
                "the picker never closed on anything — its part was already there")

pnpdrv.done()
