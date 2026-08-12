#!/bin/bash
# pnptask: the machine comes up unhomed; the home pin and manual jog (D18/D25).
. ../pnpdrv.sh

pnp_start
pnp_run ./test.py
