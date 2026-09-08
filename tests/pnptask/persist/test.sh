#!/bin/bash
# pnptask: the tracked world surviving a restart (D6, §7.2/§7.6).
#
# Three servers on the same config: one that does the work, one that restarts on
# top of the state it left, and one that starts with the state wiped — the last
# is what keeps the second's assertions from passing vacuously.
. ../pnpdrv.sh

pnp_start --log server1.log
pnp_run ./write.py
pnp_stop

pnp_start --keep-state --log server2.log
pnp_run ./restore.py
pnp_stop

pnp_start --log server3.log
pnp_run ./fresh.py
