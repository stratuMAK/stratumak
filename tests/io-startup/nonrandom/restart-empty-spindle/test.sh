#!/bin/bash
# Regression for the slot-keyed tool table (920bfb085e) on a NON-random
# changer: slot 0 is only ever a session copy of the mounted tool, but the
# SQLite store is durable — 2.9's .tbl never contained the non-random slot-0
# row (tooldata_save starts at idx 1), so every 2.9 restart began with an
# empty spindle. Without iocontrol's startup reset, the pre-restart tool
# survives in slot 0 while toolInSpindle starts at 0, and a bare G43 H0
# applies a phantom tool-length offset from a tool io reports as absent.

. ../../../stmak-driver.sh

rm -rf db
rm -f sim.var* tool.tbl
cp tool.tbl.original tool.tbl

# Boot 1: load T1 into the spindle (M61 Q1), leave it mounted at shutdown.
stmak_start_server --log server1.log test.ini
stmak_wait_ready
./drive.py load

kill $STMAK_SRV
wait $STMAK_SRV 2>/dev/null

# Boot 2: same db. The spindle must come up empty, exactly as 2.9 did.
stmak_start_server --log server2.log test.ini
stmak_wait_ready
./drive.py check
