#!/bin/bash

# simulate_probe (Tcl, removed with the hal.so binding) drove
# motion.probe-input; use `halcmd setp motion.probe-input 1` or a
# pyvcp toggle instead.
gladevcp -d -d -u probe.py -U debug=3 -H probe.hal probe.ui
