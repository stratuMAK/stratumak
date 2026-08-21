#!/bin/bash
# pnptask: busy gating, the wait position, and the auto-enable abort (D15).
. ../pnpdrv.sh

pnp_start
pnp_run ./test.py
