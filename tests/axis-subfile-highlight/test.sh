#!/bin/bash
# Preview geometry is keyed on (file, line): a sub-file's line 2 is not the
# main program's line 2. Pure client-side, no gomc-server needed.
here=$(dirname "$0")
exec python3 "$here/test.py"
