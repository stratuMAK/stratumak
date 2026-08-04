#!/bin/bash
# Classic LinuxCNC ran `exec stmakd -r interp.ini` and captured the
# interpreter's stdout into `result`; the sub's `(print, test RAN)` is a plain
# fprintf(stdout), not an operator message. Under stmak the interp runs inside
# stmakd, so that stdout lands in the server log -- cat it into our own
# stdout at the end so checkresult can grep `result` for 'test RAN' (and for
# the absence of the 'duplicate O-word label' bug).
. ../../stmak-driver.sh
stmak_start_server interp.ini
stmak_wait_ready
./test-ui.py
cat server.log
