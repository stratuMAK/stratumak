#!/bin/bash
# Regression for issue #265: "Adding threads while running GOMC Server".
#
# `halcmd newthread <name> <period>` (no explicit cpu) against a RUNNING
# gomc-server must succeed, exactly like `newthread` in a HAL file at startup.
# It used to fail with "cpu=0 is not an isolated CPU (isolated: [])" on a machine
# with no isolated CPUs: the omitted nullable cpu argument was flattened to 0
# across the cgo REST boundary instead of the -1 "auto-assign" sentinel that the
# HAL-file parser uses, and 0 is a non-isolated core so the RT-thread validator
# rejected it. gomc has no userspace comps / loadusr, so a resident gomc-server
# + halcmd replaces the classic halrun test.hal.
gomc-server -r -f nt.hal --serve >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

# Wait for the REST API to accept commands. Diagnostics go to stderr; stdout
# must stay the single thread name checkresult compares against `expected`.
ready=""
for i in $(seq 100); do
    if halcmd show comp 2>/dev/null | grep -q and2; then
        ready=1
        break
    fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** and2 never loaded within 10s; see $PWD/server.log" >&2; exit 1; }

# The issue #265 scenario: add a thread at runtime with no cpu argument.
if ! halcmd newthread runtime_thread 1000000; then
    echo "*** newthread failed at runtime (issue #265); see $PWD/server.log" >&2
    exit 1
fi

# Confirm the thread now really exists in the running instance.
halcmd list thread | grep -x runtime_thread
