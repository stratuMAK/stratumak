#!/bin/bash
# RTC, driven from HAL at a 10 ms cycle.
#
# The block reads no wall clock: IEC's RTC is seeded from its own PDT input and
# counts forward, so the only thing it measures is how long it has been
# running.  What is captured is therefore the advance past the preset, which
# grows with real time, and the absolute value, which has to start at the
# preset and nowhere else.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f rtcblock.so rtcblock.c
modcompile --compile rtcblock.st

# en: off for 2 cycles, on for 8, off for 2.
{
    for _ in $(seq 2); do echo 0; done
    for _ in $(seq 8); do echo 1; done
    for _ in $(seq 2); do echo 0; done
} > in.txt

fs_run rtcblock.hal
