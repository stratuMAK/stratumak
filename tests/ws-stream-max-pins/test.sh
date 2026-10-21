#!/bin/bash
# 20-pin streamer->sampler loopback through the halstreamer / halsampler
# clients: every sample must come back with all 20 values.
. "$(dirname "$0")/../hal-stream-driver.sh"
hal_start_server ws-stream-max-pins.hal
hal_feed_streamer <<DATA
1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20
-1 -2 -3 -4 -5 -6 -7 -8 -9 -10 -11 -12 -13 -14 -15 -16 -17 -18 -19 -20
DATA
hal_sample 2
hal_run
