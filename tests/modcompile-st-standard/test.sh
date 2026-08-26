#!/bin/bash
# The IEC 61131-3 standard function blocks, driven from HAL.  Compiling them is
# not the interesting part -- the runtest next door already does that.  This
# checks what they compute: which latch wins when set and reset are both true,
# that an edge output lasts exactly one cycle, and that a counter counts edges
# and not levels.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

# No STMAK_ST_LIB_PATH: `USING standard;` has to resolve through the shipped
# library directory, which is the path a user gets.
rm -f stdblocks.so stdblocks.c
modcompile --compile stdblocks.st

# clk aux
cat > in.txt <<'DATA'
0 0
1 0
1 0
0 0
1 0
1 0
1 1
0 1
1 0
0 0
1 0
0 0
DATA

fs_run stdblocks.hal
