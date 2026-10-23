#!/bin/bash
# 'show thread' on a thread with 40 functions.  The shim kept at most 32
# function names per thread and dropped the rest without a word, so a real
# machine's servo thread (50+ functions) showed up cut short.
stmakd -r -f show.hal --serve >server.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT

ready=""
for i in $(seq 100); do
    if halcmd show funct 2>/dev/null | grep -q f40; then
        ready=1
        break
    fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** show.hal never loaded within 10s; see $PWD/server.log" >&2; exit 1; }

# Function lines are "<index> <name>"; the thread header has no leading index.
halcmd show thread thread1 | awk '$1 ~ /^[0-9]+$/ {print $1, $2}'
