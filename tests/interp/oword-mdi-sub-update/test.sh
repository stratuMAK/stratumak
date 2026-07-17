#!/bin/bash
# Classic LinuxCNC ran `exec linuxcnc -r interp.ini` and captured the
# interpreter's stdout into `result`; the sub's `(print, test RAN)` is a plain
# fprintf(stdout), not an operator message. Under gomc the interp runs inside
# gomc-server, so that stdout lands in the server log -- cat it into our own
# stdout at the end so checkresult can grep `result` for 'test RAN' (and for
# the absence of the 'duplicate O-word label' bug).
. ../../gomc-driver.sh
gomc_start_server interp.ini
gomc_wait_ready
./test-ui.py
cat server.log
