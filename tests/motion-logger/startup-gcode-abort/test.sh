#!/bin/bash -e
# runtests invokes this as `bash -x test.sh`, which bypasses the shebang's -e —
# set it explicitly or a failing mid-script step (e.g. the motion-logger diff
# in test-ui.py) is silently swallowed.
set -e

# stratuMAK full-instance test: milltask -> motion-logger interceptor -> real motmod.
rm -f out.motion-logger*

stmakd -r test.ini &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# Wait for the server to load milltask, failing loudly on expiry rather than
# running test-ui.py against a server that never came up.
#
# NOT stmak_wait_ready here: this config's RS274NGC_STARTUP_CODE dispatches a rapid
# to motion while the machine is still at estop, which motmod rejects by design
# (see test-ui.py). stmak_test.wait_for_startup additionally requires s.state ==
# RCS_DONE, which that deliberate error may leave unsatisfied. test-ui.py owns the
# readiness predicate for this test — its wait_for_startup() polls interp idle +
# STATE_ESTOP — so the old trailing `sleep 0.5` is redundant and is dropped rather
# than replaced.
ready=""
for i in $(seq 100); do
    if halcmd show comp 2>/dev/null | grep -q milltask; then
        ready=1
        break
    fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** milltask never loaded within 10s" >&2; exit 1; }

./test-ui.py
