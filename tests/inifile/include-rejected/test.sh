#!/bin/bash
# The C INI reader (iniparse, behind inivar, rs274 -i and linuxcnc.ini)
# reads exactly one file. A file that uses #INCLUDE is refused instead of
# read with the directive skipped, which would silently lose its keys.
cd "$(dirname "$0")" || exit 1

echo "inivar plain: $(inivar -var LINEAR_UNITS -sec TRAJ -ini plain.ini)"
inivar -var LINEAR_UNITS -sec TRAJ -ini include.ini >/dev/null 2>&1
echo "inivar include: rc=$?"

rs274 -i plain.ini -g test.ngc >/dev/null 2>&1
echo "rs274 plain: rc=$?"
rs274 -i include.ini -g test.ngc >/dev/null 2>&1
echo "rs274 include: rc=$?"

python3 - <<'PY' 2>/dev/null
import linuxcnc
print("linuxcnc.ini plain:", linuxcnc.ini("plain.ini").find("TRAJ", "LINEAR_UNITS"))
try:
    linuxcnc.ini("include.ini")
    print("linuxcnc.ini include: opened")
except linuxcnc.error as e:
    print("linuxcnc.ini include: refused, gmi hint:", "gmi.IniFile()" in str(e))
PY
