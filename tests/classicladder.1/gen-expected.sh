#!/bin/bash
# Regenerate `expected` from the LinuxCNC 2.9 ClassicLadder engine.
# See ../classicladder.0/gen-expected.sh for the rationale.
set -e
cd "$(dirname "$0")"

# The float column is formatted with %f, which follows LC_NUMERIC. stmakd
# never calls setlocale, so filestream writes "1.000000"; generate the same.
export LC_ALL=C

ORACLE_DIR=../../src/stmak/internal/classicladder/testdata/oracle
make -s -C "$ORACLE_DIR"

# in-seq.txt columns: bit %I0, s32 %IW0, s32 %IW1, float %IF0.
# 270 = VAR_PHYS_WORD_INPUT, 300 = VAR_PHYS_FLOAT_INPUT, 50 = VAR_PHYS_INPUT.
# Sampled back: bit %Q0, s32 %QW0, s32 %QW1, float %QF0
# (280 = VAR_PHYS_WORD_OUTPUT, 310 = VAR_PHYS_FLOAT_OUTPUT, 60 = VAR_PHYS_OUTPUT).
{
    while read -r i0 iw0 iw1 if0; do
        [ -n "$i0" ] || continue
        echo "set 50 0 $i0"
        echo "set 270 0 $iw0"
        echo "set 270 1 $iw1"
        echo "set 300 0 $if0"
        echo "scan 1"
        echo "dumpnum"
    done
} < in-seq.txt | "$ORACLE_DIR/cl-oracle" numeric.clp 2>/dev/null |
    awk '/^NUM/ { printf "%s %s %s %f \n", $2, $3, $4, $5 }' > expected

echo "wrote expected ($(wc -l < expected) rows)"
