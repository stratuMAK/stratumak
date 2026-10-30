#!/bin/bash
# stratuMAK has no 'loadusr -w sleep'; drive the resident server from the shell.
. "$(dirname "$0")/../../hal-stream-driver.sh"
hal_start_server ilowpass.hal
halcmd start
getout() { halcmd getp ilowpass.out | awk '{print $NF}'; }
# Each step waits for out to reach its target instead of sleeping: a filter
# that stops short (the bug this test exists for) never gets there and the
# wait fails; a slow thread only makes it take longer.
for target in 4 0 -5; do
    halcmd setp ilowpass.in $target >/dev/null
    stmak_wait_until 10 "ilowpass.out to reach $target" '[ "$(getout)" = "$target" ]'
    getout
done
halcmd stop
