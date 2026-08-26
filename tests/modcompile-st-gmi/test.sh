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

# The other half of the type surface: an interface whose functions take a
# string and a structure.  Not loaded below -- hostmot2 is not here to provide
# it -- so this checks that it compiles and generates the right C, which is
# what the type mapping is about.
rm -f gmistructs.so gmistructs.c
modcompile --compile gmistructs.st
modcompile --preprocess gmistructs.st -o gmistructs.c

# The structure's ST type compiles to the header's own, not to a second
# declaration of it.
if ! grep -q 'hm2_serial_pkt_uart_config_t rx;' gmistructs.c; then
    echo 'gmistructs.c did not use the headers type for the interface structure'
    exit 1
fi
if grep -q 'PktUartConfig_t' gmistructs.c; then
    echo 'gmistructs.c declared its own copy of the interface structure'
    exit 1
fi
# Passed by address, since the callback takes a pointer to it, and the string
# passed as the array it is.
if ! grep -q 'pktuart_config(__comp_inst->__gmi_hm2_serial->ctx, (__comp_inst->chan), &((__comp_inst->rx))' gmistructs.c; then
    echo 'gmistructs.c does not pass the string and structures as the interface takes them'
    exit 1
fi
# A STRING is a char array of its declared length plus the terminator, and is
# initialised by copying rather than by filling every element.
if ! grep -q 'char chan\[33\];' gmistructs.c; then
    echo 'gmistructs.c did not size its STRING'
    exit 1
fi
if ! grep -q 'snprintf(inst->chan, sizeof(inst->chan)' gmistructs.c; then
    echo 'gmistructs.c did not copy its STRING initialiser'
    exit 1
fi

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
