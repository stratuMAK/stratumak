#!/bin/bash -e
# runtests runs this as `bash -x test.sh`, which bypasses the shebang -e.
set -e

. "$(dirname "$0")/../../stmak-driver.sh"

# The in-position latch cmod of motion/jerk-coord-inpos.
${SUDO} modcompile --install ../jerk-coord-inpos/inpos_latch.comp

stmak_start_server inpos.ini
stmak_wait_ready

./test-ui.py
