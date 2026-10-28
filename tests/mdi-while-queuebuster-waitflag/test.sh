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
for i in $(seq 20); do
    stmakd -r test.ini >server.log 2>&1 &
    STMAK_SRV=$!
    export STMAK_SRV
    # Readiness from the status buffer: a `grep milltask` loop (no failure
    # branch) plus a guessed sleep stood here.
    stmak_wait_ready
    ./test-ui.py
    kill $STMAK_SRV 2>/dev/null; wait 2>/dev/null || true
    if grep -qiE 'Segmentation fault|panic|SIGSEGV' server.log 2>/dev/null; then exit 1; fi
    if ! grep -qx 'post 6' server.log; then
        echo "*** run $i: the last MDI never ran (no \"post 6\" in server.log)" >&2
        exit 1
    fi
done
