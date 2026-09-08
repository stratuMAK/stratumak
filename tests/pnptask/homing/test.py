#!/usr/bin/env python3
"""Homing on request (D25) and the manual mode that needs it (D18, D21).

AUTOHOME is what lets a *job* home the machine, and cycle/ asserts that. This is
the other half: the explicit `home` request, which is what an AUTOHOME = 0
machine has to be homed with, and which is accepted in manual mode as well as
auto — a PLC that wants the machine homed before its first job should not have
to drop auto-enable to ask.
"""

import pnpdrv

# Long enough that a jog running at the speed set below would have moved the
# machine several millimetres — which is what makes "it did not move" mean
# something.
JOG_WOULD_HAVE_MOVED = 25  # control cycles

m = pnpdrv.Machine()
m.reset_sim()

pnpdrv.check(not m.get("homed"), "the machine comes up unhomed")

# ── manual mode ──────────────────────────────────────────────────────────────

m.set("auto-enable", False)

# A job is refused in manual mode, and the refusal completes the handshake so
# the PLC is not left waiting (§6.4).
err = m.run_job(10, 20)
pnpdrv.check_eq(err, 20, "a job in manual mode is refused with AUTO_DISABLED")
m.clear_error()

# The jog pins are ignored while unhomed (D18) — the position pins prove it,
# because a jog that ran would move them.
before = m.get("picker.0.pos-x")
m.set("jog-speed", 100)
m.set("jog-x-pos", True)
m.cycles(JOG_WOULD_HAVE_MOVED)
m.set("jog-x-pos", False)
pnpdrv.check_eq(m.get("picker.0.pos-x"), before, "an unhomed machine ignores the jog pins")

# ── the home request ─────────────────────────────────────────────────────────

m.pulse("home")
m.wait(lambda s: s["homed"], "the machine to finish homing")
pnpdrv.check(m.get("homed"), "a home pulse homes the machine in manual mode (D25)")

# ── jogging, and the teach pins ──────────────────────────────────────────────

m.set("jog-x-pos", True)
m.wait(lambda s: s["picker.0.pos-x"] > 20, "the X jog to move the picker")
m.set("jog-x-pos", False)
m.wait_still()

pos = m.snapshot()
pnpdrv.check(pos["picker.0.pos-x"] > 20, "a homed machine jogs (D18)")
pnpdrv.check_eq(pos["picker.0.pos-x"], pos["picker.0.pos-x-mu"],
                "on a metric machine the mm and machine-unit teach pins agree (D26)")
pnpdrv.check_eq(pos["picker.0.pos-x"], pos["picker.1.pos-x"],
                "both pickers report the same position while their offsets are 0")

# ── back to auto ─────────────────────────────────────────────────────────────

m.set("auto-enable", True)
m.fill_tray(10, step=0)
err = m.run_job(10, 20)
pnpdrv.check_eq(err, 0, "a job on the already-homed machine completes")
pnpdrv.check(m.get("homed"), "the machine is still homed afterwards")

# The jog pins are inert in auto mode (§6.4).
m.wait_still()
at = m.get("picker.0.pos-x")
m.set("jog-x-neg", True)
m.cycles(JOG_WOULD_HAVE_MOVED)
m.set("jog-x-neg", False)
pnpdrv.check_eq(m.get("picker.0.pos-x"), at, "auto mode ignores the jog pins")

pnpdrv.done()
