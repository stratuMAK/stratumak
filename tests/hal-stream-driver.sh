# shellcheck shell=bash
# Shared driver for converted streaming HAL tests (sourced by each test.sh).
#
# Background: the classic sampler/streamer HAL tests used
#   loadusr -w sh <feed-streamer> ; start ; loadusr -w halsampler -n N
# from inside a self-contained test.hal.  gomc has no userspace HAL components,
# so those tests now run a resident gomc-server and drive it with the
# halstreamer/halsampler REST/WebSocket clients from this shell driver.
#
# Usage in a test.sh:
#     . "$(dirname "$0")/../hal-stream-driver.sh"      # adjust ../ depth
#     hal_start_server <halfile>
#     hal_feed_streamer <<DATA        # optional, only if the .hal has a streamer
#     ...values...
#     DATA
#     hal_sample <N>                  # connect halsampler BEFORE starting threads
#     hal_run                         # start threads, wait for the samples, stop
#
# The critical ordering: halsampler must connect before 'halcmd start', because
# gomc's sampler stream delivers live samples from connect time (it is not a
# replay of the sampler component's FIFO history).

_HAL_SRVPID=""
_HAL_SAMPLER_PID=""

hal_cleanup() {
    # Kill the background halsampler first: if a test aborts mid-sequence (before
    # hal_run's `wait`) the sampler would otherwise leak into the next test and
    # poison it (a stray WS client / held resources).
    [ -n "$_HAL_SAMPLER_PID" ] && kill "$_HAL_SAMPLER_PID" 2>/dev/null
    [ -n "$_HAL_SRVPID" ] && kill "$_HAL_SRVPID" 2>/dev/null
    wait 2>/dev/null
}

# hal_start_server <halfile>: launch a resident gomc-server on the given HAL
# file (which must set up comps/wiring/threads but NOT call 'start') and wait
# until its REST API is accepting commands.
#
# Exits the test on failure rather than returning nonzero. There is no useful
# way for a caller to carry on without a server, and returning made the check
# opt-in: most callers wrote a bare `hal_start_server foo.hal` with no `||`, and
# under `#!/bin/bash` with no `set -e` a dead server sailed straight on into the
# test body, which then reported a confusing diff instead of "no server".
hal_start_server() {
    gomc-server -r -f "$1" --serve &
    _HAL_SRVPID=$!
    trap hal_cleanup EXIT
    # Deadline, not an iteration count: each pass also forks halcmd, so 100
    # iterations of `sleep 0.1` is well over 10s of real time on a loaded runner.
    local waitend=$((SECONDS + 30))
    while [ $SECONDS -lt $waitend ]; do
        halcmd show comp >/dev/null 2>&1 && return 0
        sleep 0.1
    done
    echo "hal-stream-driver: server did not become ready within 30s" >&2
    exit 1
}

# hal_feed_streamer: read streamer values from stdin and push them into the
# streamer FIFO while threads are still stopped.
hal_feed_streamer() {
    halstreamer
}

# hal_sample <N> [extra halsampler args...]: connect halsampler for N samples
# in the background.  Must be called before hal_run so the live stream is
# subscribed before threads start.  Extra args are passed to halsampler (e.g.
# -t to prefix each line with the sample number).
hal_sample() {
    local n=$1
    shift
    halsampler "$@" -n "$n" &
    _HAL_SAMPLER_PID=$!
    # Give the WebSocket subscription time to establish before threads run.
    #
    # This sleep is a known bet, NOT a wait — deliberately left as-is because
    # there is nothing to poll. Subscription happens entirely inside the server:
    # apiserver.handleStreamUpgrade accepts the WS and calls sampler.c's
    # on_new_conn, which sets conns[idx].active and latches read_pos = write_pos.
    # None of that is observable from the outside:
    #   - no HAL pin reflects it (sampler.c exports full/enable/curr-depth/
    #     overruns/sample-num only; curr-depth is derived from active conns but is
    #     computed by the RT funct, i.e. only after `halcmd start` — too late);
    #   - no REST endpoint exposes stream conns (hal_sampler.gmi sets
    #     @rest_export false; the server tracks them in an unexported map);
    #   - nothing is logged on accept (stream_handler.go logs only accept
    #     *failures*);
    #   - halsampler itself prints nothing on connect — it reads the "cfg:"
    #     header and then blocks for samples.
    # If the subscription is missed, on_new_conn latches read_pos at the current
    # write_pos and every sample before that point is lost silently (see this
    # file's header). Closing this hole needs an upstream readiness signal — e.g.
    # a `sampler.connections` HAL pin bumped by on_new_conn, or a --ready-fd /
    # "connected" line from halsampler — which this driver could then poll.
    # Inventing a client-side predicate here would only re-time the same bet.
    sleep 0.5
}

# hal_run: start the HAL threads, wait for the sampler to collect its samples,
# then stop.  The captured samples go to stdout (i.e. the test 'result').
hal_run() {
    halcmd start
    if [ -n "$_HAL_SAMPLER_PID" ]; then
        # Bounded wait: halsampler exits once it has collected its N samples,
        # normally within a fraction of a second.  If the WS subscription was
        # missed (or the stream stalls) it would otherwise block forever and
        # hang the whole suite, so cap it and fail loudly instead.
        local i
        for i in $(seq "${_HAL_SAMPLE_TIMEOUT:-100}"); do   # 100 * 0.1s = 10s
            kill -0 "$_HAL_SAMPLER_PID" 2>/dev/null || break
            sleep 0.1
        done
        if kill -0 "$_HAL_SAMPLER_PID" 2>/dev/null; then
            echo "hal-stream-driver: halsampler did not finish within" \
                 "$(( ${_HAL_SAMPLE_TIMEOUT:-100} / 10 ))s (missed subscription?)" >&2
            kill "$_HAL_SAMPLER_PID" 2>/dev/null
        fi
        wait "$_HAL_SAMPLER_PID" 2>/dev/null
    fi
    halcmd stop
}
