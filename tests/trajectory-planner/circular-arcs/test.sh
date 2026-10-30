#!/bin/bash
# Circular-arc trajectory-planner regression test (stratuMAK).
#
# The classic circular-arcs directory is a developer profiling/tuning harness
# (operf + octave plots + interactive prompts); it has no automated pass/fail
# test.  This adds one: run a full-circle G3 arc through the stratuMAK TP, capture the
# commanded X/Y joint path every servo cycle with the filestream cmod, and have
# checkresult verify every sampled point lies on the commanded circle (so a
# broken arc — straight chords, wrong radius, missed motion — fails).
set -e
rm -f path.txt sim.var sim.var.bak

. ../../stmak-driver.sh
stmak_start_server arc.ini
# The machine must be up before the run starts: a guess at readiness here let
# the script's first commands reach a task that was not ready yet.
stmak_wait_ready

./run-arc.py
stmak_check_filestream
# The EXIT trap stops the server, so filestream flushes and closes path.txt
# before checkresult reads it.
