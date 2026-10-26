#!/bin/bash
# Twelve successive halsampler clients against one running sampler: each
# must get its sample.  Prints one line per client, "<n> ok" or "<n> FAILED".
. "$(dirname "$0")/../hal-stream-driver.sh"
hal_start_server ws-stream-many-conns.hal
halcmd start
for n in $(seq 1 12); do
    if [ "$(timeout "$(stmak_scale 10)" halsampler -n 1 2>/dev/null | wc -l)" = 1 ]; then
        echo "$n ok"
    else
        echo "$n FAILED"
    fi
done
halcmd stop
