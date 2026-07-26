#!/bin/bash
# Multi-instance client contract: two task stacks, one server.
. ../gomc-driver.sh

set -e

# db/ and the .var files are scratch the server creates; recreate them so a
# rerun cannot pass on a previous run's state.
rm -rf db
rm -f ./*.var ./*.var.bak
mkdir -p db

gomc_start_server test.ini

# Readiness is per task: gomc_wait_ready asks one instance's status buffer, and
# with no instance named "milltask" here it must be told which.
export GMC_INSTANCE=left.task
gomc_wait_ready
export GMC_INSTANCE=right.task
gomc_wait_ready

# NOT exec: that replaces this shell, and with it the EXIT trap gomc_start_server
# installed — the server would outlive the test and squat port 5080, failing
# every later test that starts one.
python3 ./test-ui.py
