#!/bin/bash
# Start from the committed parameter fixture: the classic .var-file parameter
# backend (PARAMETER_FILE_MODE=file) writes sim.var on save, so reset it from
# sim.var.pre each run to keep the test deterministic.
. ../gomc-driver.sh
rm -f sim.var
cp sim.var.pre sim.var
gomc_start_server test.ini
gomc_wait_ready
./test-ui.py
