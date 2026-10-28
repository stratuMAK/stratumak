#!/bin/bash
# Prove the siglog cmod: log a replayed sequence and decode it.  Cycle numbers
# are printed relative to the first logged cycle (the snapshot), times dropped.
. "$(dirname "$0")/../filestream-driver.sh"

# Rows 2 and 3 repeat values of the row before; only real changes are logged.
# Row 4 holds NaN-free extremes; row 5 returns to zero (-0.0 is a change).
cat > in.txt <<'DATA'
1.5 1 100 -5
1.5 1 100 -6
1.5 0 100 -6
-3.5 1 4294967295 2147483647
-0.0 0 0 -2147483648
DATA

rm -f out.siglog
fs_run siglog.hal out.siglog >/dev/null

siglog-decode -f info out.siglog | grep -v '^started'
LC_ALL=C siglog-decode -f csv out.siglog |
    LC_ALL=C awk -F, 'NR == 1 { next } !seen { base = $1; seen = 1 }
                      { printf "%d %s %s\n", $1 - base, $3, $4 }'
# the file was closed cleanly (END record present)
siglog-decode out.siglog | grep -c '^# end of file' || true
grep -o "include pattern '[^']*' matches no signal" server.log | sort -u
