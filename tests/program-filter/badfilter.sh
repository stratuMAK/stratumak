#!/bin/sh
# A converter that cannot handle its input. Writes a partial program first, so
# the test can tell that a failed conversion leaves nothing openable behind.
echo "G21"
echo "cannot read source: bad magic number" >&2
exit 3
