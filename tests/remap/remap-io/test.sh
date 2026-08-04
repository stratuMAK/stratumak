#!/bin/bash -e
# remap-io: verify M62-M68 can be remapped through NGC subs and that the remap
# body can recursively call the original M-code.  The classic test also ran a
# Python-remap variant (test-py.ini); stmak removed the embedded Python interp, so
# only the NGC variant remains (the Python remap is a genuine removal).
. ../../stmak-driver.sh
export PYTHONUNBUFFERED=1
stmak_start_server test-ngc.ini
stmak_wait_ready
./test-ui.py
