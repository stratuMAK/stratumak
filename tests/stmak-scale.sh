# shellcheck shell=bash
# Small shared shell helpers for the stratuMAK test suite: deadline scaling
# (stmak_scale), EXIT-trap composition (stmak_add_exit_trap) and the
# address-in-use diagnosis (stmak_bind_failure). Source it, don't execute it.
#
# --- Deadline scaling ---
# One knob for every shell-side deadline in the stratuMAK test suite, so the bespoke
# `for i in $(seq N); do ... sleep; done` loops honour the same multiplier the
# Python waiters already do (STMAK_TEST_TIMEOUT_SCALE, see lib/python/stmak_test.py).
#
# Why it matters: the ThreadSanitizer nightly sets STMAK_TEST_TIMEOUT_SCALE=4
# (.github/workflows/nightly-stmak.yml). Under tsan a server/cmod can take several
# times longer to load, so a hardcoded 10s readiness loop would time out and fail
# the test on timing — masking the very data race the nightly exists to surface.
# Scaling these loops keeps them off that failure mode.
#
# Usage (source it, don't execute):
#     . "$(dirname "$0")/../stmak-scale.sh"      # adjust ../ depth
#     for i in $(seq "$(stmak_scale 100)"); do ... ; sleep 0.1; done
#     waitend=$((SECONDS + $(stmak_scale 30)))
#
# stmak_scale <base> echoes base*scale as an integer (minimum base). The scale is
# read from STMAK_TEST_TIMEOUT_SCALE, defaulting to 1 and clamping any non-integer
# or sub-1 value to 1 (a fractional "3.5" is truncated to 3) so the arithmetic
# below can never fail or shorten a deadline.
stmak_scale() {
    local base="$1" scale="${STMAK_TEST_TIMEOUT_SCALE:-1}"
    scale=${scale%%.*}                                  # tolerate "4.0"
    case "$scale" in '' | *[!0-9]*) scale=1 ;; esac     # non-integer → 1
    [ "$scale" -ge 1 ] 2>/dev/null || scale=1
    echo $((base * scale))
}

# --- EXIT-trap composition ---
# stmak_add_exit_trap <cmd> arranges for <cmd> to run on EXIT IN ADDITION to any
# EXIT trap already installed, newest first. bash REPLACES an EXIT trap rather
# than chaining, so a plain `trap ... EXIT` in a test — or a second sourced
# driver that sets its own trap — would otherwise silently drop an earlier
# cleanup and orphan the background stmakd it was meant to kill. Every
# driver that backgrounds a process registers its cleanup through here so the
# combination is always safe regardless of source/trap order.
stmak_add_exit_trap() {
    local cmd="$1" existing
    existing=$(trap -p EXIT)
    if [ -n "$existing" ]; then
        # existing is: trap -- 'CMDS' EXIT — pull CMDS back out and prepend ours.
        existing=${existing#trap -- \'}
        existing=${existing%\' EXIT}
        trap -- "${cmd}; ${existing}" EXIT
    else
        trap -- "$cmd" EXIT
    fi
}

# --- REST address diagnosis ---
# stmak_bind_failure [logfile] prints a diagnosis if the server died because its
# REST address was taken, and returns 0 in that case.
#
# Every test binds the same well-known REST address, so one leaked server fails
# every test that follows it. runtests checks the address once before the suite
# starts, but a server leaked by an *earlier test* slips past that check — and
# the symptom each later test reports ("server did not become ready") says
# nothing about the cause, which sits in a per-test server.log nobody reads
# until the whole run has been misread as a code regression once. The server
# already logged the reason; surface it where the failure is printed.
stmak_bind_failure() {
    local logfile="${1:-server.log}"
    [ -f "$logfile" ] || return 1
    grep -q "address already in use" "$logfile" || return 1
    echo "*** the REST address (${GMC_REST_ADDR:-127.0.0.1:5080}) was already in use," \
         "so this server exited instead of starting." >&2
    echo "*** a stmakd leaked by an earlier test is the usual cause;" \
         "every later test that starts one fails the same way." >&2
    return 0
}
