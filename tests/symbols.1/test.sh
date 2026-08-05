#!/bin/bash
# Symbol export test: test_use1 must resolve a symbol exported by test_define1.
# Drive a resident stmakd, run one servo cycle's worth, and read the two
# outputs (checkresult expects define.out nonzero, use.out zero).
. "$(dirname "$0")/../hal-stream-driver.sh"
# Build the test comps into cmod/ first — a fresh checkout (CI) doesn't have
# them; never rely on leftovers of an earlier run (that's how this passed
# locally while failing on every fresh runner).
${SUDO} modcompile --install test_define1.comp
${SUDO} modcompile --install test_use1.comp
hal_start_server dotest.hal || exit 1
halcmd start

# Wait for the servo thread to have actually run a cycle, instead of sleeping 1s
# and hoping. test_define1.out is 0 until its funct runs, so a nonzero value IS
# the "one cycle has elapsed" signal checkresult is about to assert on. Failing
# here (rather than falling through) keeps a never-started thread from being
# reported as "Value0 should be nonzero".
defout=0
deadline=$(( SECONDS + 30 ))
while [ "$SECONDS" -lt "$deadline" ]; do
    defout=$(halcmd getp test_define1.out 2>/dev/null | awk '{print $NF}')
    case "$defout" in
        ''|0) ;;
        *) break ;;
    esac
    sleep 0.05
done
case "$defout" in
    ''|0) echo "*** test_define1.out never went nonzero: the servo thread never ran" >&2
          exit 1 ;;
esac

echo "$defout"
halcmd getp test_use1.out | awk '{print $NF}'
