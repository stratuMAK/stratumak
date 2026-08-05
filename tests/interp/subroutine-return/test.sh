#!/bin/bash
. ../../stmak-driver.sh
cp -f orig.ngc test.ngc
cp -f subs/orig-sub.ngc subs/sub.ngc
stmak_start_server --log /tmp/stmak-subret.log interp.ini
stmak_wait_ready
./test-ui.py
