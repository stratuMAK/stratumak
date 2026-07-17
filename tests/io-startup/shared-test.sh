#!/bin/bash

. ../../../gomc-driver.sh

rm -f tool.tbl
cp tool.tbl.original tool.tbl

# gomc-server does not launch the [DISPLAY] program; drive it ourselves.
gomc_start_server --inherit test.ini
gomc_wait_ready
../../test-ui.py
