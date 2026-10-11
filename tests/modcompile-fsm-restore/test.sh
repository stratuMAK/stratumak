#!/bin/bash
# Drives the fsm_restore component through the io state_var restore, the
# output blocks and <fsm>_reset_outputs() in the servo thread, one input line
# per cycle (filestream), and captures the pins.
set -e
# Build the test comp into cmod/ first -- never rely on a leftover.
${SUDO} modcompile --install fsm_restore.comp
. "$(dirname "$0")/../filestream-driver.sh"
fs_run fsm.hal
