#!/bin/sh
# The EtherCAT master's data-ok cycle count, compiled on its own from the
# driver's header (the sim bus cannot lose a chosen number of cycles).
DRV="$(cd "$(dirname "$0")/../../../src/hal/drivers/ethercat" && pwd)"
gcc -O -Wall -Werror -std=gnu99 -I"$DRV" test.c -o test || exit 1
./test; exitval=$?
rm -f test
exit $exitval
