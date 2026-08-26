#!/bin/bash
# IEC's string functions, driven from HAL.
#
# Each check is a comparison against the answer written out, so what is
# asserted is the result and not merely its length. The last column takes a
# count from the stream, so the clamp runs on a value the compiler cannot see.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f strfuncs.so strfuncs.c
modcompile --compile strfuncs.st

# How many characters to take. The string is seven long, so 0 and 99 are the
# two ends of the clamp and 3 is an ordinary answer.
cat > in.txt <<'DATA'
3
0
99
7
DATA

fs_run strfuncs.hal
