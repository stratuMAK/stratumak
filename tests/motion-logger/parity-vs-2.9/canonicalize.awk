# canonicalize.awk — reduce a motion-logger capture to a common canonical
# MOTION stream so that a diff between the classic LinuxCNC 2.9 milltask and the
# stmak milltask shows only REAL behavioural differences, not format spelling or
# NML-vs-GMI init/config noise.
#
# Accepts EITHER dialect:
#   * classic 2.9 `src/emc/motion-logger/motion-logger.c`  (the checked-in
#     tests/motion-logger/*/expected.* gold in the 2.9 tree)
#   * stmak interceptor cmod `src/emc/motion-logger/motion_logger_cmod.c`
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
#       different schemes (2.9 = running canonical-op counter, stmak = per-move);
#       id is a GUI current-line tracker, not a motion parameter.
#   SET_VEL               : keep `vel=`, drop the 2.9-only `, ini_maxvel=` tail.
#   SET_SPINDLESYNC       : keep `sync=`, drop the trailing field (2.9 `flags=`
#       vs stmak `motion_type=` are incomparable encodings).
#   SET_CIRCLE            : multi-line; drop the 2.9-only `pos:` continuation
#       (the stmak cmod does not log it), keep center/normal/id(stripped).
#
# unit_factor (machine-units -> mm)
# ---------------------------------
# stmak runs the motion stream in millimetres end to end, while 2.9 emits it in
# the machine's units ([TRAJ]LINEAR_UNITS — inch for the current corpus). Run
# the ORACLE side with `-v unit_factor=25.4` to bring its length-dimensioned
# fields to mm; the stmak side runs with factor 1. Scaled per opcode:
#   SET_LINE     : x,y,z,u,v,w (positions) and vel,ini_maxvel,acc (dynamics —
#                  2.9's toExtVel emits TO_EXT_LEN for any move with a linear
#                  component; see the pure-angular caveat below)
#   SET_CIRCLE   : center x,y,z + the vel,ini_maxvel,acc continuation
#                  (normal: is a unit vector — never scaled; arcs are always
#                  cartesian so TO_EXT_LEN applies)
#   SET_VEL      : vel        SET_ACC : acc
#   SET_TERM_COND: tolerance  SET_SPINDLESYNC : sync (length per revolution)
#   PROBE / RIGID_TAP : currently log no fields in either dialect
# Angular fields (a,b,c) are degrees in both trees — never scaled.
#
# PURE-ANGULAR CAVEAT: for a move with ONLY angular displacement 2.9 emits its
# dynamics via TO_EXT_ANG (factor 1), not TO_EXT_LEN, so the x25.4 would be
# wrong. The corpus has no such move; rather than guess, we detect one (no
# linear delta since the previous SET_LINE, nonzero angular delta) and inject a
# loud marker line so the diff fails and a human adjudicates.
#
# Float formatting: scaled values are printed with %.9g; rounding for
# comparison is applied downstream (normalize.sh), not here.
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

# scale_keys(line, keys): multiply the value of every `k=<num>` field whose key
# k is listed in `keys` (comma-delimited, wrapped in commas) by unit_factor.
# Fields are ", "-separated; the first may carry an "OP " prefix which is kept.
function scale_keys(line, keys,    n, parts, i, eq, k, v, pre, out, sep) {
    if (unit_factor == 1 || unit_factor == "" ) return line
    n = split(line, parts, ", ")
    out = ""
    for (i = 1; i <= n; i++) {
        pre = ""
        kv = parts[i]
        if (i == 1 && kv !~ /^[a-z_]+=/) {      # leading "OP " or "center: " prefix
            eq = match(kv, /[a-z_]+=[^ ]*$/)
            if (eq == 0) { out = kv; continue } # no field on this fragment
            pre = substr(kv, 1, RSTART - 1)
            kv = substr(kv, RSTART)
        }
        eq = index(kv, "=")
        k = substr(kv, 1, eq - 1)
        v = substr(kv, eq + 1)
        if (index(keys, "," k ",") > 0)
            v = sprintf("%.9g", (v + 0) * unit_factor)
        sep = (i == 1) ? "" : ", "
        out = out sep pre k "=" v
    }
    return out
}

# field(line, key): numeric value of `key=` on the line, or 0 if absent.
function field(line, key,    re) {
    re = "(^|[ ,])" key "=-?[0-9.eE+-]+"
    if (match(line, re) == 0) return 0
    re = substr(line, RSTART, RLENGTH)
    sub(/^.*=/, "", re)
    return re + 0
}

BEGIN {
    started = (strip_preamble ? 0 : 1)
    if (unit_factor == "") unit_factor = 1
    have_prev = 0
    POSK = ",x,y,z,u,v,w,"
    DYNK = ",vel,ini_maxvel,acc,"
}

# Continuation lines of a multi-line record (SET_CIRCLE) start with whitespace.
/^[ \t]/ {
    if (started && in_circle) {
        line = $0
        if (line ~ /pos:/) next             # 2.9-only continuation: drop
        if (line ~ /center:/) line = scale_keys(line, POSK)
        if (line ~ /id=/) {
            sub(/id=-?[0-9]+, /, "", line)  # strip id on the id= continuation
            line = scale_keys(line, DYNK)
        }
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

    if (op=="SET_CIRCLE:") { in_circle = 1; have_prev = 0; print; next }

    line = $0
    if (op=="SET_LINE") {
        sub(/ id=-?[0-9]+,/, "", line)           # drop id field
        if (unit_factor != 1) {
            # Pure-angular detection (see header): dynamics would need factor 1.
            lin = 0; ang = 0
            if (have_prev) {
                if (field(line,"x")!=px || field(line,"y")!=py || field(line,"z")!=pz \
                 || field(line,"u")!=pu || field(line,"v")!=pv || field(line,"w")!=pw) lin = 1
                if (field(line,"a")!=pa || field(line,"b")!=pb || field(line,"c")!=pc) ang = 1
                if (ang && !lin)
                    print "### PURE-ANGULAR MOVE: dynamics unit factor not applicable — adjudicate manually"
            }
            px=field(line,"x"); py=field(line,"y"); pz=field(line,"z")
            pa=field(line,"a"); pb=field(line,"b"); pc=field(line,"c")
            pu=field(line,"u"); pv=field(line,"v"); pw=field(line,"w")
            have_prev = 1
            line = scale_keys(line, POSK DYNK)
        }
    }
    else if (op=="SET_VEL") {
        sub(/,.*/, "", line)                     # drop ini_maxvel tail
        line = scale_keys(line, ",vel,")
    }
    else if (op=="SET_ACC")          line = scale_keys(line, ",acc,")
    else if (op=="SET_TERM_COND")    line = scale_keys(line, ",tolerance,")
    else if (op=="SET_SPINDLESYNC") {
        sub(/,.*/, "", line)                     # drop flags/motion_type tail
        line = scale_keys(line, ",sync,")
    }
    else if (op=="SET_OFFSET")       line = scale_keys(line, POSK)
    print line
}
