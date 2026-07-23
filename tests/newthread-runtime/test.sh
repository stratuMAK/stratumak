#!/bin/bash
# Regression for issue #265 ("Adding threads while running GOMC Server") and the
# same nullable-scalar-argument class in addf.
#
# Two runtime halcmd operations that used to break because an omitted nullable
# i32 argument is flattened to 0 across the cgo REST dispatch boundary (the C
# int32_t ABI has no "absent"), instead of the sentinel the .hal parser uses:
#   1. `newthread <name> <period>` (no cpu): flattened cpu 0 is a non-isolated
#      core → rejected "cpu=0 is not an isolated CPU" on a no-isolcpus machine
#      (the parser default is -1 = auto). This was issue #265.
#   2. `addf <funct> <thread>` (no position): flattened position 0 = insert at
#      FRONT instead of append (the parser/impl default is -1 = append), so a
#      second addf would land before the first — silently wrong function order.
#
# It also covers the fp default of the same command: `newthread <name> <period>`
# with no fp/nofp argument must create an FP thread, like the .hal parser default
# (NewThreadToken{FP: 1}) and classic halcmd. Defaulting to nofp made every addf
# of a floating-point function fail on a runtime-created thread — hence the
# floating-point `scale` function below alongside the nofp and2/not.
#
# gomc has no userspace comps / loadusr, so a resident gomc-server + halcmd
# replaces the classic halrun test.hal. stdout must stay exactly the lines
# `expected` compares against; diagnostics go to stderr.
gomc-server -r -f nt.hal --serve >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

ready=""
for i in $(seq 100); do
    if halcmd show comp 2>/dev/null | grep -qw scale; then
        ready=1
        break
    fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** components never loaded within 10s; see $PWD/server.log" >&2; exit 1; }

# (1) Add a thread at runtime with no cpu argument (issue #265).
if ! halcmd newthread runtime_thread 1000000; then
    echo "*** newthread failed at runtime (issue #265); see $PWD/server.log" >&2
    exit 1
fi

# (2) addf three functions with no position — they must APPEND in order.
# scale is a floating-point function: addf'ing it fails outright ("function
# requires FP but thread is not FP") if the runtime thread defaulted to nofp.
for f in and2 not scale; do
    if ! halcmd addf $f runtime_thread; then
        echo "*** addf $f failed at runtime; see $PWD/server.log" >&2
        exit 1
    fi
done

# Emit the thread name, then its function names in thread order. Front-insertion
# (the bug) would reverse the two functions.
halcmd list thread | grep -x runtime_thread
halcmd show thread | sed -n '/^runtime_thread/,/^$/p' | awk '/^ *[0-9]+ /{print $2}'
