#!/bin/bash -e
# remap-io: verify M62-M68 can be remapped through NGC subs and that the remap
# body can recursively call the original M-code.  The classic test also ran a
# Python-remap variant (test-py.ini); gomc removed the embedded Python interp, so
# only the NGC variant remains (the Python remap is a genuine removal).
. ../../gomc-driver.sh
export PYTHONUNBUFFERED=1
gomc_start_server test-ngc.ini
gomc_wait_ready
./test-ui.py
