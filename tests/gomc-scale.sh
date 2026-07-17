# shellcheck shell=bash
# Small shared shell helpers for the gomc test suite: deadline scaling
# (gomc_scale) and EXIT-trap composition (gomc_add_exit_trap). Source it, don't
# execute it.
#
# --- Deadline scaling ---
# One knob for every shell-side deadline in the gomc test suite, so the bespoke
# `for i in $(seq N); do ... sleep; done` loops honour the same multiplier the
# Python waiters already do (GOMC_TEST_TIMEOUT_SCALE, see lib/python/gomc_test.py).
#
# Why it matters: the ThreadSanitizer nightly sets GOMC_TEST_TIMEOUT_SCALE=4
# (.github/workflows/nightly-gomc.yml). Under tsan a server/cmod can take several
# times longer to load, so a hardcoded 10s readiness loop would time out and fail
# the test on timing — masking the very data race the nightly exists to surface.
# Scaling these loops keeps them off that failure mode.
#
# Usage (source it, don't execute):
#     . "$(dirname "$0")/../gomc-scale.sh"      # adjust ../ depth
#     for i in $(seq "$(gomc_scale 100)"); do ... ; sleep 0.1; done
#     waitend=$((SECONDS + $(gomc_scale 30)))
#
# gomc_scale <base> echoes base*scale as an integer (minimum base). The scale is
# read from GOMC_TEST_TIMEOUT_SCALE, defaulting to 1 and clamping any non-integer
# or sub-1 value to 1 (a fractional "3.5" is truncated to 3) so the arithmetic
# below can never fail or shorten a deadline.
gomc_scale() {
    local base="$1" scale="${GOMC_TEST_TIMEOUT_SCALE:-1}"
    scale=${scale%%.*}                                  # tolerate "4.0"
    case "$scale" in '' | *[!0-9]*) scale=1 ;; esac     # non-integer → 1
    [ "$scale" -ge 1 ] 2>/dev/null || scale=1
    echo $((base * scale))
}

# --- EXIT-trap composition ---
# gomc_add_exit_trap <cmd> arranges for <cmd> to run on EXIT IN ADDITION to any
# EXIT trap already installed, newest first. bash REPLACES an EXIT trap rather
# than chaining, so a plain `trap ... EXIT` in a test — or a second sourced
# driver that sets its own trap — would otherwise silently drop an earlier
# cleanup and orphan the background gomc-server it was meant to kill. Every
# driver that backgrounds a process registers its cleanup through here so the
# combination is always safe regardless of source/trap order.
gomc_add_exit_trap() {
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
