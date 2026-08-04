#!/bin/bash
# Drive a resident stmakd to feed abs through a streamer/sampler.
# See ../filestream-driver.sh for the mechanism (stratuMAK has no userspace comps).
. "$(dirname "$0")/../filestream-driver.sh"
cat > in.txt <<DATA
0
0.25
-0.25
1
-1
64
-64
DATA
fs_run abs.hal
