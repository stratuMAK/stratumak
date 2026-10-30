#!/bin/bash
# The sampler stream never loses samples silently.  Each sampler records its
# own sample number, so a line "<n> <value>" from halsampler -t must satisfy
# value - n = constant: the numbers halsampler prints jump by exactly what was
# lost.  "lossy" must lose (exit 1, every gap on stderr, the total adding up),
# "roomy" must not (exit 0, nothing on stderr, numbers contiguous).
. "$(dirname "$0")/../hal-stream-driver.sh"
hal_start_server ws-stream-loss.hal
halcmd start

# check <name> <n>: capture n samples and print a verdict per property
check() {
    local name=$1 n=$2 rc=0 lines bad missing reported
    timeout "$(stmak_scale 20)" halsampler -t -n "$n" "$name" >"$name.out" 2>"$name.err" || rc=$?
    echo "$name: exit $rc"

    # lines, lines whose value is off its number, numbers skipped
    read -r lines bad missing < <(LC_ALL=C awk '
        NR == 1 { off = $2 - $1 }
        $2 - $1 != off { bad++ }
        { last = $1 }
        END { print NR, bad + 0, last + 1 - NR }' "$name.out")
    if [ "$lines" != "$n" ]; then
        echo "$name: $lines lines, want $n"
    elif [ "$bad" != 0 ]; then
        echo "$name: $bad samples do not match their number"
    else
        echo "$name: samples match their numbers"
    fi
    if [ "$missing" -gt 0 ]; then
        echo "$name: missing some"
    else
        echo "$name: missing none"
    fi

    reported=$(LC_ALL=C awk '/samples lost in total/ { print $2 }' "$name.err")
    if [ "${reported:-0}" = "$missing" ]; then
        echo "$name: stderr accounts for every missing sample"
    else
        echo "$name: stderr reports ${reported:-0} lost, the numbers miss $missing"
        cat "$name.err"
    fi
}

check lossy 300
check roomy 3000
halcmd stop
