#!/bin/sh
# Stand-in for a real converter (image-to-gcode and friends): reads the source
# it is handed and writes G-code on stdout, reporting progress on stderr in the
# classic FILTER_PROGRESS protocol.
#
# Halfway through it waits for the test to create filter.gate next to it.
# A converter that returned instantly would let the test pass even if the
# controller ran it inline and blocked every client for the duration, which
# is the thing under test; held at 50 %, the conversion is something the test
# can look at for as long as it needs, and an open that blocked could not
# return before the test released it.  The 50 s fallback (inside the
# controller's FILTER_TIMEOUT) only ends a run whose open did block.
gate="$(dirname "$0")/filter.gate"
x=$(cat "$1")
echo "FILTER_PROGRESS=0" >&2
echo "FILTER_PROGRESS=50" >&2
i=0
while [ ! -e "$gate" ] && [ $i -lt 500 ]; do
    sleep 0.1
    i=$((i + 1))
done
echo "FILTER_PROGRESS=100" >&2
echo "G21 G61"
echo "G1 X$x F600"
echo "G1 Y5"
echo "M2"
