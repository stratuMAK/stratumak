#!/bin/bash
# pnptask: lift, travel and descent as one stream (Z_CORNER_RADIUS, D52) in the real TP.
. ../pnpdrv.sh

# The shared config with the curve shaper on, Z corner arcs of 10 mm, station
# 21 moved behind the first dead zone, and the commanded X/Y/Z captured every
# servo cycle. Derived here, next to this test (stmakd reads files next to the
# INI only), so the other scenarios keep separate strokes.
cp ../pnptask.hal ../zones.dxf ../zones_alt.dxf .
sed -e 's|^HALFILE = pnptask.hal|&\nHALFILE = capture.hal|' \
    -e "s|^BLEND_TOLERANCE = .*|&\nPATH_SHAPER = spline\nZ_CORNER_RADIUS = ${ZCR:-10}|" \
    -e '/^\[PNPTASK_PROC_1\]/,/^\[/{s|^X = .*|X = 470.0|;s|^Y = .*|Y = 320.0|}' \
    ../pnptask.ini > zarcs.ini
rm -f path.txt

pnp_start --ini zarcs.ini
pnp_run ./test.py
