#!/bin/bash
# stmakui embedded in a Tk container (AXIS webapp tabs and panel): the page
# loads once the server answers, keys the page uses stay in it, the rest reach
# Tk with a release for every press, and a click into Tk takes the keyboard
# back. Runs its own X server; no stmakd needed.
here=$(dirname "$0")

# -displayfd picks a free display and needs no xauth (unlike xvfb-run).
coproc XVFB { Xvfb -displayfd 1 -screen 0 800x600x24 -nolisten tcp 2>/dev/null; }
read -r -t 20 display <&"${XVFB[0]}" || { echo "Xvfb did not start" >&2; exit 1; }
trap 'kill $XVFB_PID 2>/dev/null; wait $XVFB_PID 2>/dev/null' EXIT

# The page is a local test page. WebKit's bubblewrap sandbox cannot start
# inside many CI containers, and the X server has no GL for compositing.
export DISPLAY=":$display"
export WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1
export WEBKIT_DISABLE_COMPOSITING_MODE=1
python3 "$here/test.py"
