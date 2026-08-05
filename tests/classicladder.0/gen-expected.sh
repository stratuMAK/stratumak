#!/bin/bash
# Regenerate `expected` from the LinuxCNC 2.9 ClassicLadder engine.
#
# The expectations in this test are not our own output frozen in place: they are
# what the original engine does with the same ladder and the same input
# sequence.  Run this after changing in-seq.txt (or the ladder), and only when
# you intend the expectation to move.
#
#   ./gen-expected.sh
#
# Needs the 2.9 reference sources in src/hal/classicladder; see
# src/stmak/internal/classicladder/testdata/oracle.
set -e
cd "$(dirname "$0")"

ORACLE_DIR=../../src/stmak/internal/classicladder/testdata/oracle
make -s -C "$ORACLE_DIR"

# One scan per input row, applying the five bit inputs first — the same order
# the HAL thread uses (filestream.write, classicladder.0.refresh,
# filestream.read).
{
    while read -r i0 i1 i2 i3 i4; do
        [ -n "$i0" ] || continue
        echo "set 50 0 $i0"
        echo "set 50 1 $i1"
        echo "set 50 2 $i2"
        echo "set 50 3 $i3"
        echo "set 50 4 $i4"
        echo "scan 1"
        echo "dump"
    done
} < in-seq.txt | "$ORACLE_DIR/cl-oracle" estop.clp 2>/dev/null |
    awk '/^OUTPUTS/ { printf "%s %s %s \n", $2, $3, $4 }' > expected

echo "wrote expected ($(wc -l < expected) rows)"
