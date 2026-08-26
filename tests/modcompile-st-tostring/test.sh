#!/bin/bash
# The *_TO_STRING family, driven from HAL.
#
# Every check compares against the text written out, so what is asserted is
# the characters and not merely that something was produced. The component
# reports the first check that did not hold rather than one pin per check,
# which is what lets the list grow without the HAL file growing with it.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f tostring.so tostring.c
modcompile --compile tostring.st

# Numbers whose written form has a known length: 1, -1, 0, 2147483647 and
# -2147483648 are 1, 2, 1, 10 and 11 characters.
cat > in.txt <<'DATA'
1
-1
0
2147483647
-2147483648
DATA

fs_run tostring.hal
