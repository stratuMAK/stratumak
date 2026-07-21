#!/bin/bash
# gomc EtherCAT integration test — the `ethercat` REST CLI (M3).
#
# Runs the diagnostic CLI against the resident server on a two-slave sim bus
# (an output/RxPDO slave and an input/TxPDO slave) and asserts its output
# matches the IgH tool's format: master summary, slave listing, and — the
# regression guard for the fixed direction bug — `pdos` labelling the output SM
# RxPDO and the input SM TxPDO (not both TxPDO), with no "(Inputs)" annotation.
# Failure is signalled by the exit value.
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }

deadline=$(( SECONDS + $(gomc_scale 20) ))
while [ $SECONDS -lt $deadline ]; do
    [ "$(getp ethercat.0.all-op)" = TRUE ] && break
    sleep 0.1
done

fail=0
[ "$(getp ethercat.0.all-op)" = TRUE ] || { echo "FAIL: master did not reach OP" >&2; fail=1; }

check() { # <description> <text-that-must-appear> <actual-output>
    if printf '%s\n' "$3" | grep -qF "$2"; then
        echo "ok: $1"
    else
        echo "FAIL: $1 — expected to find [$2] in:" >&2
        printf '%s\n' "$3" >&2
        fail=1
    fi
}
absent() { # <description> <text-that-must-NOT-appear> <actual-output>
    if printf '%s\n' "$3" | grep -qF "$2"; then
        echo "FAIL: $1 — did not expect [$2] in output" >&2
        fail=1
    else
        echo "ok: $1"
    fi
}

master=$(ethercat master 2>&1)
check "master: slave count" "Slaves: 2" "$master"

slaves=$(ethercat slaves 2>&1)
check "slaves: slave 0 in OP"   "0  0:0  OP" "$slaves"
check "slaves: slave 1 in OP"   "1  0:1  OP" "$slaves"

# Regression guard for the pdos direction fix: output SM -> RxPDO, input -> TxPDO.
pdos0=$(ethercat pdos -p0 2>&1)
check  "pdos p0: output SM is RxPDO 0x1600" "RxPDO 0x1600" "$pdos0"
absent "pdos p0: no (Inputs) annotation"    "(Inputs)"     "$pdos0"
absent "pdos p0: output PDO not mislabelled TxPDO" "TxPDO 0x1600" "$pdos0"

pdos1=$(ethercat pdos -p1 2>&1)
check  "pdos p1: input SM is TxPDO 0x1a00"  "TxPDO 0x1a00" "$pdos1"

# cstruct/xml carry the same SM-direction logic as pdos — guard those too:
# output slave 0 -> EC_DIR_OUTPUT / <RxPdo>, input slave 1 -> EC_DIR_INPUT / <TxPdo>.
cstruct0=$(ethercat cstruct -p0 2>&1)
check "cstruct p0: SM2 sync is EC_DIR_OUTPUT" "{2, EC_DIR_OUTPUT," "$cstruct0"
xml0=$(ethercat xml -p0 2>&1)
check "xml p0: output SM is RxPdo Sm=2" '<RxPdo Sm="2">' "$xml0"
xml1=$(ethercat xml -p1 2>&1)
check "xml p1: input SM is TxPdo Sm=3"  '<TxPdo Sm="3">' "$xml1"

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim CLI: OK"
exit $fail
