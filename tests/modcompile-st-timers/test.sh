#!/bin/bash
# The IEC timers, driven from HAL at a known cycle.  What is being checked is
# what they compute over time: that a delay-on waits, that a delay-off holds,
# that a pulse is a pulse and not a level, and that a retentive timer keeps
# what it accumulated.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f timers.so timers.c
modcompile --compile timers.st

# go: low for 4 cycles, high for 20, low for the rest.  At 1 ms a cycle that
# is 4 ms off, 20 ms on, 16 ms off, against timers set to 5 ms and 10 ms.
{
    for _ in $(seq 4);  do echo 0; done
    for _ in $(seq 20); do echo 1; done
    for _ in $(seq 16); do echo 0; done
} > in.txt

fs_run timers.hal
