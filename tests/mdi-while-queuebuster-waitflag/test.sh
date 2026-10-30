#!/bin/bash -e
# Fire the M400 queue-buster interleaved with plain MDIs, 20 times, and fail if
# the stmakd ever crashes.  Only scan server.log: runtests runs this under
# `bash -x` with stderr captured, so grepping the test's own stderr would match
# the traced grep-pattern text itself (false positive).  A real crash — Go panic
# or C SIGSEGV — lands in server.log.
#
# Each run must also have executed its last MDI ("post 6" printed): a run
# whose MDIs were refused or dropped crashes nothing, and passed without
# testing the queue-buster at all.
set -e
. "$(dirname "$0")/../stmak-driver.sh"
# Each run starts its own server and kills it below; the trap is for the runs
# that do not get there: under set -e a failing test-ui.py (or a server that
# never came up) exits the script, and a server left running would fail every
# test after this one with "address already in use". STMAK_SRV is read when
# the trap runs, so it names the current server; a server already killed
# makes the kill fail harmlessly.
stmak_add_exit_trap 'kill $STMAK_SRV 2>/dev/null || true; wait 2>/dev/null || true'
for i in $(seq 20); do
    stmakd -r test.ini >server.log 2>&1 &
    STMAK_SRV=$!
    export STMAK_SRV
    # Readiness from the status buffer: a `grep milltask` loop (no failure
    # branch) plus a guessed sleep stood here.
    stmak_wait_ready
    ./test-ui.py
    # || true: a server that crashed is already gone, and the grep below is
    # what says so.
    kill $STMAK_SRV 2>/dev/null || true; wait 2>/dev/null || true
    if grep -qiE 'Segmentation fault|panic|SIGSEGV' server.log 2>/dev/null; then exit 1; fi
    if ! grep -qx 'post 6' server.log; then
        echo "*** run $i: the last MDI never ran (no \"post 6\" in server.log)" >&2
        exit 1
    fi
done
