#!/bin/bash
# stratuMAK EtherCAT integration test — startup SDO configuration (initcmds).
#
# The slave config carries <sdoConfig idx="0x2000" subIdx="0x0"> with data
# 44 33 22 11, so the master writes 0x2000:00 = 0x11223344 to the slave via CoE
# during PREOP as it brings the bus to OP. The test then reads that object back
# with the ethercat REST CLI and confirms the configured value is present (the
# sim preset it to 0), proving the lcec init-SDO path end to end — and, as a
# bonus, exercising the stratuMAK `ethercat` CLI + REST/GMI surface against the
# resident server. Failure is signalled by this script's exit value.
. "$(dirname "$0")/../../hal-stream-driver.sh"

hal_start_server ethercat.hal
halcmd start

getp() { halcmd getp "$1" 2>/dev/null | awk '{print $NF}'; }

deadline=$(( SECONDS + $(stmak_scale 15) ))
while [ $SECONDS -lt $deadline ]; do
    [ "$(getp ethercat.0.all-op)" = TRUE ] && break
    sleep 0.1
done

fail=0
[ "$(getp ethercat.0.all-op)" = TRUE ] || { echo "FAIL: master did not reach OP" >&2; fail=1; }

# Read back the startup-configured object 0x2000:00 via the CLI (default
# STMAK_REST_URL = the resident server). The XML wrote 0x11223344 = 287454020;
# the sim preset it to 0, so a match proves the init SDO was applied.
val=$(ethercat -p0 --type uint32 upload 0x2000 0x00 2>&1)
echo "upload 0x2000:00 -> $val"
echo "$val" | grep -qw 287454020 \
    || { echo "FAIL: startup SDO not applied (expected 0x11223344, got: $val)" >&2; fail=1; }

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim SDO config: OK"
exit $fail
