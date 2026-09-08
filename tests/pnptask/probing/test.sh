#!/bin/bash
# pnptask: slot probing corrects the tracked tray state, and exhausts it (D9).
. ../pnpdrv.sh

pnp_start
pnp_run ./test.py
