#!/bin/bash
# pnptask: the error ids the pins carry, and the error-reset that clears them.
. ../pnpdrv.sh

pnp_start
pnp_run ./test.py
