#!/bin/bash
# fperiod inside a FUNCTION_BLOCK resolves through the RT task rather than
# through the HAL function's period argument, because a block body is its own C
# function and never receives that argument.  This runs one block from two
# threads a decade apart and captures what each saw.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f cycletime.so cycletime.c
modcompile --compile cycletime.st

# filestream needs a replay stream to drain; nothing here reads it, the sample
# count is what ends the run.
for _ in $(seq 16); do echo 0; done > in.txt

fs_run cycletime.hal
