#!/bin/bash
# Drives the fsm_multi component (two fsms) through the fsm execution model in
# the servo thread, one input line per cycle (filestream), and captures every
# pin.
set -e
# Build the test comp into cmod/ first -- never rely on a leftover.
${SUDO} modcompile --install fsm_multi.comp
. "$(dirname "$0")/../filestream-driver.sh"
fs_run fsm.hal
