#!/bin/bash
# stratuMAK EtherCAT integration test — CoE <initCmds> parsing.
#
# Regression cover for three defects in conf_icmds.c that only surface on real
# vendor exports:
#
#   * <Data> that crosses the parser's 8 KiB read boundary. Expat splits the
#     node into two character-data chunks and the split can fall between the
#     two nibbles of a byte; parse_data() used to call parseHex() on each chunk
#     on its own and reject the dangling nibble with "Invalid data". The
#     fixture places the 0x2000 payload so the break lands after an odd number
#     of hex digits, and the readback proves the byte was reassembled.
#   * CompleteAccess="true" as TwinCAT spells it, where atoi() alone yields 0.
#   * A <Transition> the driver cannot honour, which used to abort the parse.
#
# Failure is signalled by this script's exit value.
. "$(dirname "$0")/../../hal-stream-driver.sh"

# Guard the fixture: if the file is ever reflowed the split moves and the test
# silently stops exercising the boundary case.
python3 - "$(dirname "$0")/initcmds.xml" <<'PY' || exit 1
import re, sys
B = 8192
data = open(sys.argv[1], 'rb').read()
m = [x for x in re.finditer(rb'<Data>([0-9a-fA-F]+)</Data>', data)][-1]
s, e = m.start(1), m.end(1)
if s // B == (e - 1) // B:
    sys.exit("FAIL: fixture no longer straddles the %d byte boundary" % B)
off = (s // B + 1) * B - s
if off % 2 == 0:
    sys.exit("FAIL: fixture splits between bytes, not inside one")
print("fixture: 0x2000 payload splits after %d hex digits (odd)" % off)
PY

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

# Each object is preset to 0 by the sim, so a match proves the command was
# parsed and applied. Values are little-endian in the XML.
check() { # <index> <expected-decimal> <what>
    val=$(ethercat -p0 --type uint32 upload "$1" 0x00 2>&1)
    echo "upload $1:00 -> $val"
    echo "$val" | grep -qw "$2" \
        || { echo "FAIL: $3 (expected $2, got: $val)" >&2; fail=1; }
}

check 0x2000 287454020  "init cmd whose <Data> straddles the read boundary"  # 0x11223344
check 0x2002 572662306  "init cmd with CompleteAccess=\"true\""              # 0x22222222
check 0x2004 1145324612 "init cmd with an unhonourable <Transition>"         # 0x44444444

halcmd stop >/dev/null 2>&1

[ $fail -eq 0 ] && echo "ethercat sim initCmds parsing: OK"
exit $fail
