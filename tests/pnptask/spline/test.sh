#!/bin/bash
# pnptask: a leg on a smoothed path (PATH_SHAPER = spline, D47-D50) in the real TP.
. ../pnpdrv.sh

# The shared config with the curve shaper on, process station 21 moved behind
# the first dead zone, and the commanded X/Y/Z captured every servo cycle.
# Derived here, next to this test, so the other scenarios stay on the route;
# its HAL file and drawings are copied along (stmakd reads files next to the
# INI only).
cp ../pnptask.hal ../zones.dxf ../zones_alt.dxf .
sed -e 's|^HALFILE = pnptask.hal|&\nHALFILE = capture.hal|' \
    -e 's|^BLEND_TOLERANCE = .*|&\nPATH_SHAPER = spline|' \
    -e '/^\[PNPTASK_PROC_1\]/,/^\[/{s|^X = .*|X = 470.0|;s|^Y = .*|Y = 320.0|}' \
    ../pnptask.ini > spline.ini
rm -f path.txt

pnp_start --ini spline.ini
pnp_run ./test.py
