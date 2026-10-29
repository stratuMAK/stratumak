#!/usr/bin/env python3

# Ported to the stratuMAK model: read the startup tool number directly from
# iocontrol.tool-number via halcmd (stratuMAK has no userspace HAL components, so the
# old test-ui HAL component + postgui net are gone).
#
# One read, no polling: iocontrol sets the pin in its Start, and stmakd
# starts every C module before it serves the REST API that stmak_wait_ready
# waits on.  So the value read here is the startup value -- also when it is
# 0, the pin's power-on default, which a poll could not tell from "not set
# yet".

import subprocess
import sys

with open('expected-startup-tool-number') as f:
    expected = int(f.read())
print("expecting tool number %d" % expected)

out = subprocess.check_output(["halcmd", "getp", "iocontrol.tool-number"])
got = int(float(out.split()[-1]))
if got != expected:
    print("Error: iocontrol.tool-number is %d, expected %d" % (got, expected))
    sys.exit(1)
print("iocontrol.tool-number is %d" % expected)
