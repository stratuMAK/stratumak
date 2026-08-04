#!/bin/bash

. ../../../stmak-driver.sh

rm -f tool.tbl
cp tool.tbl.original tool.tbl

# stmakd does not launch the [DISPLAY] program; drive it ourselves.
stmak_start_server --inherit test.ini
stmak_wait_ready
../../test-ui.py
