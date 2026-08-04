#!/bin/bash
# Re-express the classic Python hal.stream overrun/underrun/sampleno test on the
# filestream cmod.  stratuMAK removed the embedded Python hal.stream binding; the ring
# semantics it exercised (mixed-type round-trip, sample counting, underrun when
# clocked empty, no overrun) are now HAL pins.  Replay 9 bfsu samples through a
# depth-10 ring clocked for 12 ticks and verify them.
cat > in.txt <<DATA
0 0 0 0
1 1 1 1
0 2 2 2
1 3 3 3
0 4 4 4
1 5 5 5
0 6 6 6
1 7 7 7
0 8 8 8
DATA
rm -f out.txt server.log
stmakd -r -f halmodule1.hal --serve >server.log 2>&1 &
SRV=$!
trap '[ -n "$SRV" ] && kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
# Deadlines below honour STMAK_TEST_TIMEOUT_SCALE via stmak_scale.
. "$(dirname "$0")/../stmak-scale.sh"
# Readiness must fail loudly: falling through clocked a dead server and reported
# "FAIL sampleno= underruns= ..." instead of "the server never started".
# stdout is compared against `expected` (a single "pass"), so this goes to stderr.
ready=""
for i in $(seq "$(stmak_scale 100)"); do
    if halcmd show comp 2>/dev/null | grep -q filestream; then ready=1; break; fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** filestream never loaded within 10s; see $PWD/server.log" >&2; exit 1; }
halcmd start
for i in $(seq "$(stmak_scale 300)"); do [ "$(halcmd getp filestream.done 2>/dev/null | awk '{print $NF}')" = TRUE ] && break; sleep 0.02; done
if [ "$(halcmd getp filestream.done 2>/dev/null | awk '{print $NF}')" != TRUE ]; then
    echo "*** filestream.done never went TRUE within 6s — the replay stalled;" \
         "sample counts below will be short; see $PWD/server.log" >&2
fi
sn=$(halcmd getp filestream.sample-num | awk '{print $NF}')
un=$(halcmd getp filestream.underruns  | awk '{print $NF}')
ov=$(halcmd getp filestream.overruns   | awk '{print $NF}')
halcmd stop
kill $SRV 2>/dev/null; wait $SRV 2>/dev/null; SRV=""

# sampleno counts every clocked sample; underruns once per empty clock (12-9=3);
# overruns never (the reader keeps up); and the ring round-trips all 9 samples.
if [ "$sn" = 12 ] && [ "$un" = 3 ] && [ "$ov" = 0 ] && diff -q out.txt capture.golden >/dev/null; then
    echo pass
    exit 0
fi
echo "FAIL sampleno=$sn underruns=$un overruns=$ov"
diff out.txt capture.golden
exit 1
