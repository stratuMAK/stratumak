#!/bin/bash
# The fsm-block rewrite of multiclick (multiclick_fsm.comp) on tests/multiclick's
# inputs and expected output: the fsm implementation must reproduce the
# hand-written state machine cycle for cycle.
set -e
# filestream refuses an infile outside the test directory, so the input is a
# copy; it must stay the original's.
cmp -s input-signals ../multiclick/input-signals ||
    { echo "input-signals differs from tests/multiclick/input-signals" >&2; exit 1; }
# Build the test comp into cmod/ first -- never rely on a leftover.
${SUDO} modcompile --install multiclick_fsm.comp
. "$(dirname "$0")/../filestream-driver.sh"
fs_run multiclick.hal
