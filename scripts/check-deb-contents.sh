#!/bin/bash
# Assert the stratumak Debian packages contain what they claim.
#
# Usage: check-deb-contents.sh <dir-with-debs>
#
# Verifies the artifact, not the build log. Every check corresponds to a
# defect that once shipped silently: the build succeeded, dh_install saw
# nothing wrong, and the package was broken anyway (empty locale tree,
# dropped MIME registration, capability-blind RUNPATH, stale install
# sources). See PRODUCTION_READINESS.md, status log 2026-08-03.
set -u
DIR=${1:?usage: check-deb-contents.sh <dir-with-debs>}
fail=0
note() { echo "FAIL: $*" >&2; fail=1; }
ok() { echo "  ok: $*"; }

main=$(ls "$DIR"/stratumak_*.deb 2>/dev/null | head -1)
dev=$(ls "$DIR"/stratumak-dev_*.deb 2>/dev/null | head -1)
[ -n "$main" ] || { echo "FAIL: no stratumak_*.deb in $DIR" >&2; exit 1; }
[ -n "$dev" ] || note "no stratumak-dev deb"

C=$(dpkg-deb -c "$main")

# Controller binary and the pristine/local-rebuild symlink chain.
echo "$C" | grep -q './usr/libexec/stratumak/stmakd$' \
    && ok "stmakd in libexec" || note "stmakd missing from libexec"
echo "$C" | grep -q 'usr/bin/stmakd -> /var/lib/stratumak/bin/stmakd' \
    && ok "bindir symlink chain" || note "usr/bin/stmakd symlink wrong/missing"
# Nothing under /var may be dpkg-owned: postinst creates it, so an upgrade
# can never unpack over a locally rebuilt server.
echo "$C" | awk '{print $6}' | grep -q '^\./var/' \
    && note "package ships paths under /var (postinst owns them)" || ok "no /var paths in package"

# Translations: install-locale once fell off the install graph with no error
# because install-dirs pre-creates the locale tree.
mo=$(echo "$C" | grep -c 'usr/share/locale/.*/LC_MESSAGES/.*\.mo$')
[ "$mo" -ge 5 ] && ok "$mo .mo files present" || note "only $mo .mo files - install-locale regressed"

# MIME registration: a dangling sharedmimeinfo symlink is silently skipped
# by dh_installmime.
echo "$C" | grep -q 'usr/share/mime/packages/stratumak.xml' \
    && ok "MIME registration shipped" || note "text/ngc MIME file missing"

# mesa_modbus was removed in favour of hm2_modbus; nothing of it may ship.
echo "$C$(dpkg-deb -c "$dev" 2>/dev/null)" | grep -qE 'mesa-modcompile|mesa_modbus\.c\.tmpl' \
    && note "mesa-modcompile remnants shipped" || ok "no mesa_modbus remnants"
echo "$C" | grep -q 'cmod/hm2_modbus.so' && ok "hm2_modbus.so shipped" || note "hm2_modbus.so missing"
echo "$C" | grep -q 'usr/bin/mesambccc' && ok "mesambccc in base" || note "mesambccc missing"

# The hostmot2 secondary cmods resolve hostmot2.so via DT_RPATH. RUNPATH and
# $ORIGIN are both ignored under AT_SECURE, i.e. under exactly the file
# capabilities the postinst applies to stmakd.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
dpkg-deb -x "$main" "$tmp"
for so in hm2_modbus setsserial; do
    f=$(find "$tmp" -name "$so.so" | head -1)
    if [ -z "$f" ]; then note "$so.so not found"; continue; fi
    d=$(readelf -d "$f")
    echo "$d" | grep -q 'RUNPATH' && note "$so.so has RUNPATH (ignored under AT_SECURE)"
    echo "$d" | grep -q '\$ORIGIN' && note "$so.so rpath contains \$ORIGIN (ignored under AT_SECURE)"
    echo "$d" | grep -q 'RPATH.*linuxcnc/cmod' \
        && ok "$so.so absolute DT_RPATH" || note "$so.so lacks absolute cmod DT_RPATH"
done

# Kernel-era fossils must stay gone: rtapi.conf has no reader since the
# migration, and modules/ was the old rtlib location (cmods live in cmod/).
echo "$C" | grep -q 'etc/stratumak/rtapi.conf' && note "dead rtapi.conf shipped" || ok "no rtapi.conf"
echo "$C" | awk '{print $6}' | grep -q 'linuxcnc/modules/' && note "ghost modules/ directory shipped" || ok "no modules/ ghost dir"

# Web UIs are served from EMC2_WEBAPP_DIR; without them every tool is a 404.
echo "$C" | grep -q 'usr/share/stratumak/webapp/halshow/' && ok "webapps shipped" || note "webapps missing"

# Documentation language set: de/en/es/fr, Norwegian dropped by ruling.
ls "$DIR"/stratumak-doc-nb_* >/dev/null 2>&1 && note "stratumak-doc-nb exists despite nb drop" || ok "no nb doc package"
find "$tmp" -name '*_nb.pdf' | grep -q . && note "nb PDFs shipped" || ok "no nb PDFs"
for l in de en es fr; do
    ls "$DIR"/stratumak-doc-"${l}"_* >/dev/null 2>&1 && ok "doc-$l built" || note "stratumak-doc-$l missing"
done

# -dev must let an integrator build cmods/gomods on the target.
if [ -n "$dev" ]; then
    D=$(dpkg-deb -c "$dev")
    echo "$D" | grep -q 'usr/bin/modcompile' && ok "modcompile in -dev" || note "modcompile missing from -dev"
    echo "$D" | grep -q 'usr/share/stratumak/stmak/' && ok "stmak tree in -dev" || note "stmak source tree missing from -dev"
    dpkg-deb -f "$dev" Depends | grep -q 'libc6' \
        && ok "-dev has shlibs deps" || note "-dev lacks libc6 (shlibs:Depends regressed)"
fi

# Maintainer scripts must exist under the real package name; a postinst keyed
# to a name that never existed simply never runs.
dpkg-deb -e "$main" "$tmp/ctrl"
[ -f "$tmp/ctrl/postinst" ] && grep -q setcap "$tmp/ctrl/postinst" \
    && ok "postinst applies caps" || note "postinst missing or lacks setcap"
[ -f "$tmp/ctrl/postrm" ] && ok "postrm present" || note "postrm missing"

echo
[ "$fail" -eq 0 ] && echo "ALL CHECKS PASSED" || echo "CHECKS FAILED"
exit $fail
