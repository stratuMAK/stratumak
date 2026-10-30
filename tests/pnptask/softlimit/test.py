#!/usr/bin/env python3
"""A joint pushed past its soft limit by hand, and the way back.

The amps being off is exactly when an operator *can* move a joint by hand, so
this is not an exotic state: it is the normal way an axis ends up outside its
soft limits. Two things have to hold afterwards.

Nothing is wrong yet. While motion is disabled the joint's command is not a
command at all -- it is slaved to the feedback, so that enabling does not jump
the machine. Measuring that mirror against the soft limits reports a
trajectory-planner fault ("Exceeded POSITIVE soft limit") for something the
trajectory planner did not do, and the machine still has to come back on: the
operator needs the axis powered to drive it out again.

And the way back has to exist. Being outside a limit means one direction is
forbidden and the other is the only way home. A jog *back toward* the valid
range must move; a jog further out must not.
"""

import pnpdrv

X = 0                 # joint 0
X_MAX = 600.0         # [JOINT_0]MAX_LIMIT
OUTSIDE = X_MAX + 20.0

m = pnpdrv.Machine()
m.reset_sim()

# Home first: an unhomed joint has no soft limits to exceed, and the whole
# question is what a *homed* machine does about a hand.
m.pulse("home")
m.wait(lambda s: s["homed"], "the machine to home")

# ── the hand ────────────────────────────────────────────────────────────────

m.set("machine-on", False)
m.wait(lambda s: not s["machine-is-on"], "the machine to go off")

log_before = pnpdrv.log_text()
m.hand_hold(X, OUTSIDE)
# Motion has to have sampled the axis where it now stands before the check
# means anything: the disabled machine's command tracks the feedback, so the
# command reaching OUTSIDE is that sample. Then a second of the module's own
# cycles: the operator's hand is not a transient.
m.wait(lambda s: abs(m.joint_pos(X) - OUTSIDE) < 1e-6,
       "motion to take the pushed axis as its position")
m.cycles(100)

pnpdrv.check("soft limit" not in pnpdrv.log_text()[len(log_before):],
             "a hand on a disabled axis is not reported as a soft-limit fault",
             lambda: "logged: " + " / ".join(
                 l.split('msg=')[-1] for l in pnpdrv.log_text()[len(log_before):].splitlines()
                 if "soft limit" in l))

# ── the machine still comes back on ─────────────────────────────────────────

m.set("machine-on", True)
m.wait(lambda s: s["machine-is-on"], "the machine to come back on")
pnpdrv.check(m.get("machine-is-on"),
             "the machine re-enables with a joint outside its limits")

# The hand lets go; the axis stays where it was pushed.
m.hand_release(X)
m.cycles(5)
pnpdrv.check(m.joint_pos(X) > X_MAX,
             "the axis really is outside its soft limit",
             lambda: "joint 0 at %.2f, limit %.1f" % (m.joint_pos(X), X_MAX))

# ── the way back ────────────────────────────────────────────────────────────

m.set("auto-enable", False)      # manual mode: the jog pins are live
m.set("jog-speed", 50.0)

# Jogged until the axis is well inside again: that is the way home the check
# is about, and the jog toward the limit below needs to start inside it.
INSIDE = X_MAX - 5.0
m.set("jog-x-neg", True)
m.wait(lambda s: m.joint_pos(X) < INSIDE, "the jog to bring the axis back inside")
m.set("jog-x-neg", False)
m.wait_still()
pnpdrv.check(m.joint_pos(X) < INSIDE,
             "jogging back toward the valid range moves the axis",
             lambda: "joint 0 at %.2f" % m.joint_pos(X))

# And the limit still holds from the inside. A teleop jog targets the limit
# itself, so "further out" is not something a jog can even ask for -- what has
# to be true is that jogging toward the limit stops at it.
m.set("jog-x-pos", True)
m.wait(lambda s: m.joint_pos(X) >= X_MAX - 0.01, "the jog to reach the limit")
# Still held: a jog that did not stop at the limit would carry on past it.
m.cycles(100)
m.set("jog-x-pos", False)
m.wait_still()
pnpdrv.check(m.joint_pos(X) <= X_MAX + 0.01,
             "jogging toward the limit stops at it",
             lambda: "joint 0 ended at %.3f, limit %.1f" % (m.joint_pos(X), X_MAX))

pnpdrv.done()
