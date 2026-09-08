#!/usr/bin/env python3
"""The manual-handling retention round trip of §8.1.

An operator who opens a loaded picker by hand has taken the part into their own
hands, and the next close is what decides where it went: onto the part (the
record comes back) or onto nothing (the part is theirs and the record is gone).
Between the two the world is in limbo — the picker is neither loaded nor free —
so every job is refused until the question is answered.

Retention is a reservation, not a memory: that refusal is the whole point of it.
"""

import pnpdrv


def loaded(machine):
    """Which pickers are commanded closed — the module's claim on a part."""
    return [n for n in (0, 1) if machine.get("picker.%d.close" % n)]


m = pnpdrv.Machine()
m.reset_sim()
m.fill_tray(10, step=0)

# ── set up a swap, so the retained record carries an obligation too ──────────

pnpdrv.check_eq(m.run_job(10, 20), 0, "a part reached the press")
pnpdrv.check_eq(m.run_job(10, 20), 0, "a second job swapped it out for a fresh one")

held = loaded(m)
pnpdrv.check_eq(len(held), 1, "one picker holds the swapped-out part")
pk = held[0]

pnpdrv.check_eq(m.run_job(10, 21), 19,
                "with the swap obligation standing, other origins are refused")
m.clear_error()

# ── the operator opens the picker ────────────────────────────────────────────

m.set("auto-enable", False)
m.pulse("picker.%d.manual-open" % pk)
m.wait(lambda s: not s["picker.%d.close" % pk], "the picker to open")
m.wait_gripper(pk, False)
pnpdrv.check(not m.holding(pk), "the gripper physically let the part go")

m.set("auto-enable", True)
pnpdrv.check_eq(m.run_job(20, 21), 22,
                "a retained record refuses every job, including the obligated one")
m.clear_error()

# A second open press is idempotent — it does not turn the reservation into a
# free picker.
m.set("auto-enable", False)
m.pulse("picker.%d.manual-open" % pk)
m.set("auto-enable", True)
pnpdrv.check_eq(m.run_job(20, 21), 22, "pressing open again changes nothing")
m.clear_error()

# ── the operator puts the part back ──────────────────────────────────────────

m.set("auto-enable", False)
m.pulse("picker.%d.manual-close" % pk)
m.wait(lambda s: s["picker.%d.close" % pk], "the picker to close")
m.wait_manual_judged()
m.set("auto-enable", True)

pnpdrv.check(m.holding(pk), "the gripper closed onto the part")
pnpdrv.check_eq(m.run_job(10, 21), 19,
                "the record is back, obligation and all — other origins refused again")
m.clear_error()

# The pick is skipped because the record names station 20, so a gripper that
# grips nothing cannot spoil it.
m.miss(0, -1)
m.miss(1, -1)
pnpdrv.check_eq(m.run_job(20, 21), 0, "and the obligated job places the restored part")
pnpdrv.check(m.get("proc.21.has-material"), "the test station received it")
m.reset_sim()

# ── the other verdict: the operator keeps the part ───────────────────────────

pnpdrv.check_eq(m.run_job(10, 20), 0, "another swap loads a picker again")
held = loaded(m)
pnpdrv.check_eq(len(held), 1, "a picker holds the swapped-out part")
pk = held[0]

m.set("auto-enable", False)
m.pulse("picker.%d.manual-open" % pk)
m.wait(lambda s: not s["picker.%d.close" % pk], "the picker to open")

# The part is in the operator's hands now, so the close finds nothing — which is
# what the sim's miss injection is: a gripper that meets its own jaws.
#
# On this verdict the close output is high only from the press to the
# judgement — one pick-settle-time, ~120 ms, most of it already spent inside
# pulse() — so watching for the transient high is a race a polled driver
# loses on a loaded host. The judgement itself is the observable: the module
# only judges a close it commanded, so the verdict landing in the log proves
# the picker closed without racing the pin.
m.miss(pk, -1)
m.pulse("picker.%d.manual-close" % pk)
m.wait_manual_judged()
pnpdrv.check(not m.holding(pk), "the gripper gripped nothing")

# §8.1's workflow ends here: gripping nothing IS the verdict, so the module
# reopens the picker itself — no further press, the machine comes back ready
# to pick.
m.wait(lambda s: not s["picker.%d.close" % pk], "the verdict to reopen the picker")
m.wait_gripper(pk, False)
m.set("auto-enable", True)
m.reset_sim()

pnpdrv.check_eq(m.run_job(10, 21), 0,
                "the record is gone with the part: jobs run again, obligation and all")

pnpdrv.done()
