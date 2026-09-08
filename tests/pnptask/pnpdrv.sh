# shellcheck shell=bash
#
# Server lifecycle for the pnptask scenario tests (phase 7, D27). Source it from
# a scenario directory:
#
#     . ../pnpdrv.sh
#     pnp_start                 # fresh machine: persisted state wiped
#     ./test.py
#
# Why this exists rather than stmak-driver.sh alone: stmak_wait_ready asks a
# milltask status buffer over GMI, and pnptask has no GMI surface at all — its
# whole interface is HAL pins (D12). Readiness here is machine-is-on going high,
# which is the module having resolved its motion stack, pushed the configuration
# and completed the enable handshake.
#
# All scenarios share one config (../pnptask.ini), and stmakd chdirs to the INI's
# directory, so the persisted state lands in ../db no matter which scenario
# directory the test runs from. That is why wiping it is this file's job and not
# runtests' per-testdir `rm -rf db`.

. "$(dirname "${BASH_SOURCE[0]}")/../stmak-driver.sh"

PNP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PNP_INI="$PNP_DIR/pnptask.ini"

# pnp_wipe_state
#
# Remove the persisted tray/station/held-material state, so a scenario starts
# against a machine with empty trays, empty stations and free pickers.
pnp_wipe_state() {
    rm -rf "$PNP_DIR/db"
}

# pnp_start [--keep-state] [--log FILE]
#
# Start stmakd on the shared config and block until pnptask has the machine on.
# Wipes the persisted state first unless --keep-state is given — which is what
# the persistence scenario uses for its second start.
pnp_start() {
    local wipe=1 log=()
    PNP_LOG="server.log"
    while true; do
        case "$1" in
            --keep-state) wipe=0; shift ;;
            --log) log=(--log "$2"); PNP_LOG="$2"; shift 2 ;;
            *) break ;;
        esac
    done
    [ "$wipe" -eq 1 ] && pnp_wipe_state

    stmak_start_server "${log[@]}" "$PNP_INI"
    pnp_wait_ready
}

# pnp_wait_ready [timeout_seconds]
pnp_wait_ready() {
    local timeout="${1:-60}"

    if [ -n "$STMAK_SRV" ] && ! kill -0 "$STMAK_SRV" 2>/dev/null; then
        stmak_bind_failure
        stmak_fail "server exited before it became ready; see $PWD/${PNP_LOG:-server.log}"
    fi

    if ! python3 -c '
import sys
sys.path.insert(0, sys.argv[1])
import pnpdrv
pnpdrv.Machine().wait_ready(timeout=float(sys.argv[2]))
' "$PNP_DIR" "$timeout"; then
        stmak_bind_failure
        stmak_fail "pnptask did not bring the machine up within ${timeout}s; see $PWD/${PNP_LOG:-server.log}"
    fi
}

# pnp_stop
#
# Stop the server and wait for it to be gone, so a following pnp_start can bind
# the REST port again. The EXIT trap stmak_start_server installed stays in place
# and simply finds the process already dead.
pnp_stop() {
    [ -n "$STMAK_SRV" ] || return 0
    kill "$STMAK_SRV" 2>/dev/null
    wait "$STMAK_SRV" 2>/dev/null
    STMAK_SRV=""
}

# pnp_run <driver.py>
#
# Run a scenario driver with this directory importable. NOT exec: that would
# replace the shell and with it the EXIT trap that kills the server.
pnp_run() {
    PYTHONPATH="$PNP_DIR${PYTHONPATH:+:$PYTHONPATH}" python3 "$@"
}
