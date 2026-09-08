#!/usr/bin/env python3
"""An estop landing in the middle of a job, and the way back (D14, §6.2).

Estop is one of the two things that can end a running job (D16). Because a job
runs *inside* the control loop, ticking the same cycle through every wait
(§6.5), it ends wherever it stands — no action has to remember that. What the
estop leaves behind is the deliberate part: motion disabled, every picker
released, every fixture's release request withdrawn, and a machine that does not
come back until machine-on has been asked for again.
"""

import pnpdrv

m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── estop mid-move ───────────────────────────────────────────────────────────

m.start_job(10, 20)
# The component tray is at y = 380..440 and the machine homes at y = 0, so a
# picker past y = 100 is one in the middle of the travel leg.
m.wait(lambda s: s["picker.0.pos-y"] > 100, "the job to get moving")

m.set("estop-on", True)
err = m.await_job()
pnpdrv.check_eq(err, 1, "the estop ended the running job with ESTOP")

s = m.snapshot()
pnpdrv.check(not s["machine-is-on"], "machine-is-on dropped")
pnpdrv.check(not s["busy"] and not s["start-job"], "the handshake completed")
pnpdrv.check(not s["picker.0.close"] and not s["picker.1.close"],
             "every picker was released — the one thing that drops held material (D14)")
m.wait_gripper(0, False)
pnpdrv.check(not m.holding(0), "the gripper physically let go")
pnpdrv.check(not s["proc.20.release"] and not s["proc.21.release"],
             "no fixture was left commanded open (D19)")

# ── clearing the estop is not a request to start moving again ────────────────

m.set("estop-on", False)
m.cycles(30)
pnpdrv.check(not m.get("machine-is-on"),
             "a machine-on still high from before the estop does not re-enable the machine")

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 1, "and a job is still refused, with the estop's own id latched")
m.clear_error()

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 2, "with the latch cleared the refusal names the real reason: MACHINE_OFF")
m.clear_error()

# ── the operator asks again ──────────────────────────────────────────────────

m.set("machine-on", False)
m.cycles(10)
m.set("machine-on", True)
m.wait(lambda s: s["machine-is-on"], "the machine to come back on")
pnpdrv.check(m.get("machine-is-on"), "cycling machine-on re-enables the machine")

err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "and it runs jobs again")
pnpdrv.check(m.get("proc.20.has-material"), "the press holds the part")

pnpdrv.done()
