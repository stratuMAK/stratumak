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
# A second of it: the operator's hand is not a transient, and motion has to
# have sampled the axis where it now stands before the check means anything.
m.cycles(100)

pnpdrv.check("soft limit" not in pnpdrv.log_text()[len(log_before):],
             "a hand on a disabled axis is not reported as a soft-limit fault",
             lambda: "logged: " + " / ".join(
                 l.split('msg=')[-1] for l in pnpdrv.log_text()[len(log_before):].splitlines()
                 if "soft limit" in l))

# ── the machine still comes back on ─────────────────────────────────────────

m.set("machine-on", True)
m.cycles(50)
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

before = m.joint_pos(X)
m.set("jog-x-neg", True)
m.cycles(50)
m.set("jog-x-neg", False)
m.cycles(10)
moved_back = before - m.joint_pos(X)
pnpdrv.check(moved_back > 1.0,
             "jogging back toward the valid range moves the axis",
             "moved %.2f mm, wanted more than 1" % moved_back)

# And the limit still holds from the inside. A teleop jog targets the limit
# itself, so "further out" is not something a jog can even ask for -- what has
# to be true is that jogging toward the limit stops at it.
m.set("jog-x-pos", True)
m.cycles(300)
m.set("jog-x-pos", False)
m.cycles(20)
pnpdrv.check(m.joint_pos(X) <= X_MAX + 0.01,
             "jogging toward the limit stops at it",
             lambda: "joint 0 ended at %.3f, limit %.1f" % (m.joint_pos(X), X_MAX))

pnpdrv.done()
