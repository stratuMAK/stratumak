#!/bin/bash
# A floating tap (G84) finishes under the feed and spindle inhibits and runs at
# its programmed feed and speed whatever the overrides say.  The servo-thread
# trace (tapping-cycle.hal) is appended to, so start clean.
#
# set -e: test-ui.py does the checking; without it a failed check ended only
# that script and the test passed on stmak_check_filestream's status.
set -e
rm -f trace.txt

. ../stmak-driver.sh
stmak_start_server tapping-cycle.ini
stmak_wait_ready
./test-ui.py
stmak_check_filestream
