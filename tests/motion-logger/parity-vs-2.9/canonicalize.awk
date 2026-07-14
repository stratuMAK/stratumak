# canonicalize.awk — reduce a motion-logger capture to a common canonical
# MOTION stream so that a diff between the classic LinuxCNC 2.9 milltask and the
# gomc milltask shows only REAL behavioural differences, not format spelling or
# NML-vs-GMI init/config noise.
#
# Accepts EITHER dialect:
#   * classic 2.9 `src/emc/motion-logger/motion-logger.c`  (the checked-in
#     tests/motion-logger/*/expected.* gold in the 2.9 tree)
#   * gomc interceptor cmod `src/emc/motion-logger/motion_logger_cmod.c`
# The two share the SET_LINE format verbatim; they differ in a handful of
# fields and in which init/config commands they emit.
#
# Policy
# ------
# KEEP only the behavioural canon/motion opcodes (moves, spindle, per-move
# dynamics, offsets, digital/analog out). DROP everything else — SET_NUM_JOINTS,
# SETUP_ARC_BLENDS, SET_*_LIMIT, SET_JOINT_*/SET_AXIS_* setup, SET_WORLD_HOME,
# JOINT_ACTIVATE, mode toggles (COORD/FREE/TELEOP), ENABLE/DISABLE, *_ENABLE,
# FEED_SCALE/RAPID_SCALE, JOG_ABORT, amplifier enable/disable, ABORT,
# SET_SPINDLE_PARAMS — that is architecture-specific bring-up/teardown plumbing,
# not motion the G-code produced.
#
# Strip fields that carry no cross-tree meaning or differ only in spelling:
#   SET_LINE / SET_CIRCLE : drop `id=N`  — the two trees number motion ids on
#       different schemes (2.9 = running canonical-op counter, gomc = per-move);
#       id is a GUI current-line tracker, not a motion parameter.
#   SET_LINE / SET_CIRCLE : drop the per-move dynamics `vel=`,`ini_maxvel=`,`acc=`.
#       gomc runs mm-everywhere (config vel/accel limits converted to mm) while
#       2.9 keeps them in inch-based machine units, so no single unit factor
#       reconciles the two: pure-linear rapids differ x25.4, G1 feeds not at all,
#       and mixed linear+angular moves diverge non-uniformly because 2.9 blends
#       mm displacements against inch/s limits (an inconsistency that mis-picks
#       the limiting axis; gomc is physically correct). These per-move dynamics
#       are therefore not a portable cross-tree quantity — compare only the
#       geometry (positions x..w), motion_type, and turn. The FULL dynamics are
#       still regression-checked in the self golds (gomc-vs-gomc). See
#       PARITY_FINDINGS.md "units (mm-everywhere)".
#   SET_VEL               : keep `vel=`, drop the 2.9-only `, ini_maxvel=` tail.
#   SET_SPINDLESYNC       : keep `sync=`, drop the trailing field (2.9 `flags=`
#       vs gomc `motion_type=` are incomparable encodings).
#   SET_CIRCLE            : multi-line; drop the 2.9-only `pos:` continuation
#       (the gomc cmod does not log it), keep center/normal/id(stripped).
#
# Float rounding is applied downstream (normalize.sh), not here.
#
# strip_preamble
# --------------
# Some captures are a single file that inlines the machine bring-up preamble
# before the program (mountaindew), instead of splitting it into its own
# expected.builtin-startup segment (basic). Run with `-v strip_preamble=1` to
# drop that prefix: output is suppressed until the first move-class opcode
# (SET_LINE/SET_CIRCLE/PROBE/RIGID_TAP/SPINDLE_ON/SET_OFFSET/SET_DOUT/SET_AOUT),
# which never appears in bring-up. NOTE: only use it for combined captures whose
# program BEGINS with a move (true for the current corpus) — it would clip a
# program that leads with a bare SET_VEL/SET_ACC before its first move.

function is_anchor(op) {
    return (op=="SET_LINE" || op=="SET_CIRCLE:" || op=="PROBE" || op=="RIGID_TAP" \
         || op=="SPINDLE_ON" || op=="SET_OFFSET" || op=="SET_DOUT" || op=="SET_AOUT")
}

function keep(op) {
    return (op=="SET_LINE" || op=="SET_CIRCLE:" || op=="PROBE" || op=="RIGID_TAP" \
         || op=="SPINDLE_ON" || op=="SPINDLE_OFF" || op=="SPINDLE_SCALE" \
         || op=="SPINDLE_ORIENT" || op=="SPINDLE_INCREASE" || op=="SPINDLE_DECREASE" \
         || op=="SPINDLE_BRAKE_ENGAGE" || op=="SPINDLE_BRAKE_RELEASE" \
         || op=="SET_SPINDLESYNC" || op=="SET_VEL" || op=="SET_ACC" \
         || op=="SET_TERM_COND" || op=="SET_OFFSET" \
         || op=="SET_DOUT" || op=="SET_AOUT")
}

BEGIN { started = (strip_preamble ? 0 : 1) }

# Continuation lines of a multi-line record (SET_CIRCLE) start with whitespace.
/^[ \t]/ {
    if (started && in_circle) {
        line = $0
        if (line ~ /pos:/) next             # 2.9-only continuation: drop
        sub(/id=-?[0-9]+, /, "", line)      # strip id on the id= continuation
        sub(/, ini_maxvel=[^,]*/, "", line) # drop per-move dynamics (as for SET_LINE)
        sub(/, vel=[^,]*/, "", line)
        sub(/, acc=[^,]*/, "", line)
        print line
    }
    next
}

{
    in_circle = 0
    op = $1
    if (!started) {                 # strip_preamble: skip bring-up until first move
        if (is_anchor(op)) started = 1
        else next
    }
    if (!keep(op)) next

    if (op=="SET_CIRCLE:") { in_circle = 1; print; next }

    line = $0
    if (op=="SET_LINE") {
        sub(/ id=-?[0-9]+,/, "", line)           # drop id field
        # Drop the per-move dynamics vel/ini_maxvel/acc. 2.9 expresses these in
        # inch-based machine units while gomc (mm-everywhere) uses mm, and no
        # single unit factor reconciles them: pure-linear rapids scale x25.4,
        # G1 feeds not at all, and mixed linear+angular moves non-uniformly
        # (2.9 blends mm displacements against inch/s limits — see PARITY_FINDINGS.md).
        # Keep the portable, cross-tree-identical fields: positions x..w,
        # motion_type, turn. The full dynamics stay in the self-regression golds.
        sub(/, ini_maxvel=[^,]*/, "", line)
        sub(/, vel=[^,]*/, "", line)
        sub(/, acc=[^,]*/, "", line)
    }
    else if (op=="SET_VEL")          sub(/,.*/, "", line)            # drop ini_maxvel tail
    else if (op=="SET_SPINDLESYNC")  sub(/,.*/, "", line)            # drop flags/motion_type tail
    print line
}
