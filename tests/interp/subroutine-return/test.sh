#!/bin/bash
. ../../gomc-driver.sh
cp -f orig.ngc test.ngc
cp -f subs/orig-sub.ngc subs/sub.ngc
gomc_start_server --log /tmp/gomc-subret.log interp.ini
gomc_wait_ready
./test-ui.py
