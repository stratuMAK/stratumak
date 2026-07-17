#!/bin/bash
# mb2hal's INI-DEBUG dump: with INIT_DEBUG=3 mb2hal echoes every parsed config
# key. In gomc mb2hal is a cmod and its diagnostics go to the server log via slog
# as: time=... level=DEBUG msg="<fnct> DEBUG: [SECTION] [KEY] [VALUE]" component=mb2hal
# Those DBG lines are debug-level, so run the server at debug (-d 0) to surface
# them, normalize back to the classic "mb2hal <fnct> DEBUG: ..." form, and compare
# the deterministic init/parse/pin-creation phase (up to "mb2hal is running").
# Shutdown logging is timing-sensitive on kill and intentionally not checked.
rm -f server.log
gomc-server -d 0 -r -f mb2hal.hal >server.log 2>&1 &
SRV=$!
trap '[ -n "$SRV" ] && kill $SRV 2>/dev/null; wait 2>/dev/null' EXIT
# Wait until mb2hal has finished starting (it logs "mb2hal is running").
# Fail loudly on expiry: falling through emitted a truncated (or empty) parse
# phase, which diffs against `expected` as a content mismatch rather than
# reporting that mb2hal never came up. stdout is compared against `expected`, so
# the diagnostic goes to stderr.
. "$(dirname "$0")/../../gomc-scale.sh"   # readiness deadline honours GOMC_TEST_TIMEOUT_SCALE
ready=""
for i in $(seq "$(gomc_scale 100)"); do
    if grep -q "mb2hal is running" server.log 2>/dev/null; then ready=1; break; fi
    kill -0 $SRV 2>/dev/null || break
    sleep 0.1
done
[ -n "$ready" ] || { echo "*** mb2hal never logged \"is running\" within 10s; see $PWD/server.log" >&2; exit 1; }
# Emit the parse phase in the classic format, stopping at the "is running" line.
grep "component=mb2hal" server.log \
  | sed -E 's/^time=[^ ]+ level=[A-Z]+ msg="(.*)" component=mb2hal$/mb2hal \1/' \
  | sed -n '1,/mb2hal is running/p'
