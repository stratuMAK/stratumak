#!/bin/bash
# INTEGRAL, DERIVATIVE, LIMITALARM and SEMA, driven with a fixed CYCLE of one
# second so the expected numbers are arithmetic rather than a measurement.
#
# x is a ramp of 2.0 per call.  That is the input DERIVATIVE's four-point form
# is designed to be exact on, so the output has to settle at exactly 2.0 -- a
# wrong coefficient shows up as a wrong number rather than as noise.
#
# It then drops to 6.0, which is between HYSTERESIS's two thresholds: a Schmitt
# trigger has to hold its state there, and a plain comparator would not.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f analog.so analog.c
modcompile --compile analog.st

# x run rst
cat > in.txt <<'DATA'
0.0 0 1
0.0 1 0
2.0 1 0
4.0 1 0
6.0 1 0
8.0 1 0
10.0 1 0
12.0 1 0
14.0 1 0
6.0 1 0
0.5 1 0
0.5 0 1
DATA

fs_run analog.hal
