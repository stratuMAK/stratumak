#!/bin/sh
# Stand-in for a real converter (image-to-gcode and friends): reads the source
# it is handed and writes G-code on stdout, reporting progress on stderr in the
# classic FILTER_PROGRESS protocol.
#
# It takes about a second on purpose. A converter that returned instantly would
# let the test pass even if the controller ran it inline and blocked every
# client for the duration, which is the thing under test.
x=$(cat "$1")
i=0
while [ $i -le 100 ]; do
    echo "FILTER_PROGRESS=$i" >&2
    i=$((i + 20))
    sleep 0.2
done
echo "G21 G61"
echo "G1 X$x F600"
echo "G1 Y5"
echo "M2"
