#!/bin/bash
. "$(dirname "$0")/../filestream-driver.sh"
cp "$(dirname "$0")/in-seq.txt" in.txt
fs_run classicladder.hal
