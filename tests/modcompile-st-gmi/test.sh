#!/bin/bash
# GMI from Structured Text, both ends.
#
# gmiprovider.st implements the `home` interface with a FUNCTION_BLOCK whose
# methods are the interface's functions.  gmiconsumer.st consumes it and calls
# through it.  They are separate modules and no HAL signal joins them: the
# consumer finds the provider through the API registry, by the instance name
# it was loaded under.
#
# What the pins are for is watching the two agree -- the consumer's reads come
# back across the interface, the provider's come from its own state, and a
# call that did not arrive shows up as the two disagreeing.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f gmiprovider.so gmiprovider.c gmiconsumer.so gmiconsumer.c
modcompile --compile gmiprovider.st
modcompile --compile gmiconsumer.st

# start step cancel
cat > in.txt <<'DATA'
0 0 0
1 0 0
0 0 0
0 1 0
0 0 0
0 1 0
0 0 0
0 0 1
0 0 0
0 0 0
DATA

fs_run gmi.hal
