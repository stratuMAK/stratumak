#!/bin/bash
# Self-test for lib/python/gomc_test.py, the module the rest of the suite now
# synchronises through. Pure Python: no server, no HAL, no motion — so it costs
# nothing and cannot itself be flaky.
set -e
./test-helpers.py
