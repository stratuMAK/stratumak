# shellcheck shell=bash
#
# Shared gomc server lifecycle + readiness helpers for runtests shell tests.
# Source it, don't execute it:
#
#     . "$(dirname "$0")/../gomc-driver.sh"     # adjust depth to suit
#     gomc_start_server test.ini
#     gomc_wait_ready
#
# Why this exists
# ---------------
# Tests used to spell readiness as:
#
#     for i in $(seq 100); do
#         halcmd show comp 2>/dev/null | grep -q milltask && break
#         sleep 0.1
#     done
#     sleep 0.5
#
# Two defects, copy-pasted across ~30 tests. First, the loop has no failure
# branch: when it expires the test carries on against a machine that never came
# up, and reports a garbage diff instead of "the server never started". Second,
# the trailing `sleep 0.5` is a guess. halcmd is itself a REST client, so it
# succeeding already proves the API is serving — what the sleep was really
# covering is the gap until the *milltask task* has initialised. That is a fact
# about the status buffer, so gomc_wait_ready asks the status buffer, using the
# same readiness predicate the Python tests use (gomc_test.wait_for_startup).
# One definition of "ready", polled against a deadline, failing loudly.
#
# Every deadline honours GOMC_TEST_TIMEOUT_SCALE (see lib/python/gomc_test.py).

gomc_fail() {
    echo "*** gomc-driver: $*" >&2
    exit 1
}

# gomc_start_server [--log FILE | --inherit] <ini> [extra gomc-server args...]
#
# Starts gomc-server in the background, exports GOMC_SRV with the pid, and
# installs an EXIT trap that kills it. Does NOT wait for readiness — call
# gomc_wait_ready next (separate so a test can do work in between, and so a test
# that starts its server some other way can still use the waiter).
#
# Where the server's output goes is the one thing that genuinely varies between
# tests, so it is the one thing this takes an option for:
#   (default)     ./server.log — what most checkresult scripts grep.
#   --log FILE    somewhere else.
#   --inherit     the test's own stdout/stderr, for tests whose expected output
#                 or checkresult reads the server's chatter from the run output.
gomc_start_server() {
    local logfile="server.log"
    while true; do
        case "$1" in
            --inherit) logfile=""; shift ;;
            --log) [ -n "$2" ] || gomc_fail "--log needs a file"; logfile="$2"; shift 2 ;;
            *) break ;;
        esac
    done
    [ $# -ge 1 ] || gomc_fail "gomc_start_server needs an ini file"

    if [ -n "$logfile" ]; then
        rm -f "$logfile"
        gomc-server -r "$@" >"$logfile" 2>&1 &
    else
        gomc-server -r "$@" &
    fi
    GOMC_SRV=$!
    export GOMC_SRV
    # shellcheck disable=SC2064  # expand GOMC_SRV now: it is what we must kill
    trap "kill $GOMC_SRV 2>/dev/null; wait 2>/dev/null" EXIT
}

# gomc_wait_ready [timeout_seconds]
#
# Blocks until the machine has finished starting up and is accepting commands.
# Fails the test (loudly, with the status buffer's actual contents) if that does
# not happen in time, or if the server exited early.
gomc_wait_ready() {
    local timeout="${1:-30}"

    if [ -n "$GOMC_SRV" ] && ! kill -0 "$GOMC_SRV" 2>/dev/null; then
        gomc_fail "server exited before it became ready; see $PWD/server.log"
    fi

    if ! python3 -c '
import sys
import gomc_test
gomc_test.wait_for_startup(gomc_test.Stat(), timeout=float(sys.argv[1]))
' "$timeout"; then
        if [ -n "$GOMC_SRV" ] && ! kill -0 "$GOMC_SRV" 2>/dev/null; then
            gomc_fail "server exited while starting up; see $PWD/server.log"
        fi
        gomc_fail "server did not become ready within ${timeout}s; see $PWD/server.log"
    fi
}

# gomc_wait_pin <pin> <value> [timeout_seconds]
#
# Blocks until a HAL pin reaches a value. Replaces open-coded
# `for i in $(seq ...); do halcmd ... ; sleep 0.1; done` loops that fall through
# silently on expiry.
gomc_wait_pin() {
    [ $# -ge 2 ] || gomc_fail "gomc_wait_pin needs a pin and a value"
    local pin="$1" value="$2" timeout="${3:-30}"

    python3 -c '
import sys
import gomc_test
pin, raw, timeout = sys.argv[1], sys.argv[2], float(sys.argv[3])
if raw.upper() in ("TRUE", "FALSE"):
    want = raw.upper() == "TRUE"
elif raw in ("0", "1"):
    want = raw == "1"
else:
    want = float(raw)
gomc_test.wait_pin(pin, want, timeout=timeout)
' "$pin" "$value" "$timeout" || gomc_fail "HAL pin $pin never reached $value"
}

# gomc_wait_file_contains <file> <text> [timeout_seconds] [count]
#
# Blocks until a file another process is writing contains <text> (at least
# <count> times). Replaces "sleep long enough that the log has probably been
# flushed" before grepping or diffing it.
gomc_wait_file_contains() {
    [ $# -ge 2 ] || gomc_fail "gomc_wait_file_contains needs a file and text"
    local file="$1" text="$2" timeout="${3:-30}" count="${4:-1}"

    python3 -c '
import sys
import gomc_test
gomc_test.wait_file_contains(sys.argv[1], sys.argv[2],
                             timeout=float(sys.argv[3]), count=int(sys.argv[4]))
' "$file" "$text" "$timeout" "$count" \
        || gomc_fail "$file never contained $count x $text"
}
