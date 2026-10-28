#!/bin/bash

# Ported to stratuMAK: run a resident stmakd, capture motion samples with
# filestream in the servo thread (motion-test.hal), and drive the machine with
# the rsh->gmi translator instead of piping linuxcncrsh commands into
# `nc localhost 5007`.
#
# pipefail: the waits below run inside the subshell piped into rsh2gmi.py,
# where a failed wait used to end only the subshell while the test carried on.
set -e -o pipefail

. "$(dirname "$0")/../../stmak-driver.sh"

rm -f result.halsamples

stmak_start_server motion-test.ini
stmak_wait_ready

stmak_wait_pin motion.in-position TRUE

(
    echo hello EMC mt 1.0
    echo set enable EMCTOO

    echo set mode manual
    echo set estop off
    echo set machine on

    echo set home 0
    echo set home 1
    echo set home 2

    # Wait for homing to complete
    stmak_wait_pin motion.is-all-homed TRUE

    echo set mode mdi
    # The interpreter starts in the machine's units (G20 here — inch config,
    # matching 2.9), so `g0x1` moves 1 inch. The HAL joint pins are stmak-mm:
    # wait for 25.4.
    dist=1
    dist_mm=25.4
    echo set mdi g0x$dist

    # Wait for movement to complete
    stmak_wait_pin joint.0.pos-fb $dist_mm
    stmak_wait_pin joint.0.in-position TRUE
    stmak_wait_pin joint.1.in-position TRUE

    echo shutdown
) | python3 ../../rsh2gmi.py

# Every servo cycle of the run is in result.halsamples, or the test fails here;
# the server's EXIT trap stops it, which flushes the file before checkresult.
stmak_check_filestream
echo finished capturing data
