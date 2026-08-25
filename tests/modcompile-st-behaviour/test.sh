#!/bin/bash
# Differential test: every shipped component here has a Structured Text twin,
# both are driven from the same signals, and both outputs are captured.  The
# expected file holds each pair side by side, so a difference between the two
# frontends is a diff rather than a surprise on a machine.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

# Build the twins into this directory; the .hal loads them by path.
rm -f st_twins.so st_twins.c
modcompile --compile st_twins.st

cat > in.txt <<'DATA'
0 0 0 0
0 1 0 0
1 1 0 0
1 0 0 0
0 0 1 0
0 1 1 0
1 1 1 0
1 0 1 0
0 0 0 1
0 1 0 1
1 1 0 1
1 0 0 1
0 0 1 1
0 1 1 1
1 1 1 1
1 0 1 1
DATA

fs_run differential.hal
