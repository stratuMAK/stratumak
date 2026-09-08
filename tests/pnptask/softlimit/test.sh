#!/bin/bash
# pnptask: a joint pushed past its soft limit with the amps off, and the way back.
. ../pnpdrv.sh

pnp_start
pnp_run ./test.py
