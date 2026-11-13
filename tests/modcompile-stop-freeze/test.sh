#!/bin/bash
# A component's realtime function stops executing once stmakd, shutting down,
# has stopped every module; one with "option run_after_stop" runs to the end.
# freeze_watch holds the second stop phase open and says which counter moved.
set -e
${SUDO} modcompile --install freeze_watch.comp
${SUDO} modcompile --install freeze_cnt.comp
${SUDO} modcompile --install keep_cnt.comp
. "$(dirname "$0")/../stmak-scale.sh"
rm -f server.log
stmakd -r -f freeze.hal --serve >server.log 2>&1 &
pid=$!
for i in $(seq "$(stmak_scale 100)"); do
    halcmd show comp 2>/dev/null | grep -q keep_cnt && break
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
done
sleep 0.3
kill -INT "$pid"
wait "$pid" || true
grep -o "freeze_watch: .*" server.log | sed 's/\\n.*//;s/".*//' | head -1
