#!/usr/bin/env python3
"""The error ids of §7.5, raised by the real stack and cleared by error-reset.

Every one of these is a job that fails, and what a PLC sees of it is three pins:
start-job cleared, error high, error-id saying which. The order matters — faults
latch first-error-wins (§6.5), so each one is cleared before the next is
provoked, and the last two check what happens when they are not.
"""

import pnpdrv

m = pnpdrv.Machine()
m.reset_sim()

# ── refused before the machine moves (§7.4 VALIDATE) ─────────────────────────

pnpdrv.check_eq(m.run_job(99, 20), 5, "an unknown origin-id raises INVALID_ORIGIN")
m.clear_error()

pnpdrv.check_eq(m.run_job(10, 99), 6, "an unknown dest-id raises INVALID_DEST")
m.clear_error()

pnpdrv.check_eq(m.run_job(10, 20, deadzone=7), 8,
                "a dead-zone selector past the DEADZONE_FILE list raises INVALID_DEADZONE_SELECT")
m.clear_error()

pnpdrv.check_eq(m.run_job(20, 11), 13,
                "picking from an empty process station raises PROC_NO_MATERIAL")
m.clear_error()

pnpdrv.check(not m.get("homed"),
             "none of the refusals moved the machine — it is still unhomed")

# ── a fixture that never answers ─────────────────────────────────────────────

m.fill_tray(10, step=0)
m.stick_fixture(0)

pnpdrv.check_eq(m.run_job(10, 20), 17,
                "a fixture that never reports released raises RELEASE_TIMEOUT")

s = m.snapshot()
pnpdrv.check(not s["proc.20.release"],
             "the failed action withdrew its release request (D19)")
pnpdrv.check(not s["proc.20.has-material"], "the station is not credited with the part")
pnpdrv.check(s["picker.0.close"] and m.holding(0),
             "the material is still in the picker, where the job left it")

# The part is in a picker and recorded against the tray it came from, so the
# same job re-commanded finishes it — no second pick (§8's first rule).
m.stick_fixture(0, False)
m.clear_error()
pnpdrv.check_eq(m.run_job(10, 20), 0, "with the fixture working the job completes")
pnpdrv.check_eq(m.run_job(20, 11), 0, "and the part moves on to the output tray")

# ── a gripper that never actuates ────────────────────────────────────────────

m.stick_gripper(0)
pnpdrv.check_eq(m.run_job(10, 20), 15,
                "a gripper still reporting opened after the settle raises PICKER_CLOSE_FAILED")

# First error wins: a job started with the latch set is refused, and the id on
# the pin is still the original diagnosis rather than whatever the refusal
# would have produced (§7.7).
s = m.snapshot()
pnpdrv.check(s["error"] and not s["busy"] and not s["start-job"],
             "the handshake completed with the error flag standing")
pnpdrv.check_eq(m.run_job(99, 20), 15,
                "a job started while an error is latched is refused, id unchanged")

m.clear_error()
s = m.snapshot()
pnpdrv.check(not s["error"] and s["error-id"] == 0, "error-reset clears flag and id")

pnpdrv.done()
