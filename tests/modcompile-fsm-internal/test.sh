#!/bin/bash
# Drives the fsm_internal component through an internal on (an on item
# without a target) in the servo thread, one input line per cycle
# (filestream), and captures the pins.
set -e
# Build the test comp into cmod/ first -- never rely on a leftover.
${SUDO} modcompile --install fsm_internal.comp
. "$(dirname "$0")/../filestream-driver.sh"
fs_run fsm.hal
