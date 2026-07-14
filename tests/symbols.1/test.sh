#!/bin/bash
# Symbol export test: test_use1 must resolve a symbol exported by test_define1.
# Drive a resident gomc-server, run one servo cycle's worth, and read the two
# outputs (checkresult expects define.out nonzero, use.out zero).
. "$(dirname "$0")/../hal-stream-driver.sh"
# Build the test comps into cmod/ first — a fresh checkout (CI) doesn't have
# them; never rely on leftovers of an earlier run (that's how this passed
# locally while failing on every fresh runner).
${SUDO} modcompile --install test_define1.comp
${SUDO} modcompile --install test_use1.comp
hal_start_server dotest.hal
halcmd start
sleep 1
halcmd getp test_define1.out | awk '{print $NF}'
halcmd getp test_use1.out | awk '{print $NF}'
