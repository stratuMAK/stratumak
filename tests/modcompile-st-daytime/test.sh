#!/bin/bash
# DATE and TIME_OF_DAY, driven from HAL.
#
# The block reads no clock of its own: every column is a function of that
# cycle's input, so the expected output is exact and a wrong split shows up as
# a wrong number rather than as a flaky margin.
#
# The inputs deliberately straddle 1970.  Splitting a point in time is a floor
# to midnight and a remainder, and C's remainder takes the sign of its left
# operand -- so a naive split puts a pre-epoch day one day late and runs its
# clock backwards through the day.
set -e
. "$(dirname "$0")/../filestream-driver.sh"

rm -f daytime.so daytime.c
modcompile --compile daytime.st

# One point in time per cycle, as seconds since 1970.  Written out with the
# day each one falls on, because that is what the expected output asserts and
# a bare epoch second is unreadable.
cat > in.txt <<'DATA'
1787733000      # 2026-08-26 08:30:00 -- inside the shift
1787702400      # 2026-08-26 00:00:00 -- midnight: the clock reads zero
1787788799      # 2026-08-26 23:59:59 -- one second before the next day
1767268800      # 2026-01-01 12:00:00 -- the day the last column measures from
-3600           # 1969-12-31 23:00:00 -- before the epoch, an hour before midnight
-14182980       # 1969-07-20 20:17:00 -- well before it, and off a day boundary
DATA

fs_run daytime.hal
