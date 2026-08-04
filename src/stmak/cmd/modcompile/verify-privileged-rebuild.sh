#!/bin/sh
# Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
# License: GPL Version 2
#
# Exercises the privileged half of `modcompile` on a packaged system: the parts
# that only run as root and therefore cannot be covered by the Go tests.
#
#   sudo src/stmak/cmd/modcompile/verify-privileged-rebuild.sh
#
# What it checks, in order:
#
#   1. the package's postinst left a usable state tree and build account
#   2. `modcompile rebuild` completes, and the compiler did NOT run as root
#   3. the rebuilt server replaced the symlink with a real file, root-owned,
#      carrying the capabilities the packaged one had
#   4. `modcompile add-gomod` registers a module and compiles it in
#   5. `modcompile --install` builds a cmod without the compiler being root
#   6. `modcompile rm-gomod` takes it back out again
#
# It builds stmakd three times, so allow a quarter of an hour. Nothing outside
# /var/lib/stratumak, /var/cache/stratumak-build and its own temp directory is
# written, and step 6 leaves the system as it was found.
#
# Not idempotent in one respect: if the machine already had external modules
# registered, this refuses to run rather than rebuild around them.

set -e

STATE=/var/lib/stratumak
CACHE=/var/cache/stratumak-build
BUILD_USER=stratumak-build
LIBEXEC=/usr/libexec/stratumak/stmakd

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }
step() { echo; echo "== $* =="; }

[ "$(id -u)" = 0 ] || fail "run this with sudo"

command -v modcompile >/dev/null 2>&1 || fail "modcompile is not on PATH; is stratumak-dev installed?"

# ---------------------------------------------------------------------------
step "1. what the package left behind"
# ---------------------------------------------------------------------------

for d in "$STATE" "$STATE/bin" "$STATE/cmod" "$STATE/modules" "$STATE/stmak"; do
    [ -d "$d" ] || fail "$d does not exist; did postinst run?"
    owner=$(stat -c '%U:%G %a' "$d")
    [ "$owner" = "root:root 755" ] || fail "$d is $owner, want root:root 755"
done
pass "state tree exists, root:root 0755 throughout"

getent passwd "$BUILD_USER" >/dev/null || fail "the $BUILD_USER account does not exist"
build_uid=$(id -u "$BUILD_USER")
[ "$build_uid" != 0 ] || fail "$BUILD_USER is uid 0, which defeats the point"
pass "$BUILD_USER exists as uid $build_uid"

[ -f "$LIBEXEC" ] || fail "$LIBEXEC missing"
caps=$(getcap "$LIBEXEC" | sed 's/^[^ ]* //')
[ -n "$caps" ] || fail "$LIBEXEC carries no file capabilities; postinst's setcap did not take"
pass "packaged server carries: $caps"

if [ -n "$(ls -A "$STATE/modules" 2>/dev/null)" ]; then
    fail "external modules are already registered; this script would rebuild around them.
      Remove them first, or run this on a machine that has none."
fi
pass "no external modules registered, so the run starts from a known state"

# ---------------------------------------------------------------------------
step "2. modcompile rebuild"
# ---------------------------------------------------------------------------

# The whole point: prove the compiler did not run as root. A root-owned file in
# the build identity's cache is the fingerprint of exactly that going wrong, so
# start from a clean cache and check afterwards.
rm -rf "$CACHE"
modcompile rebuild
pass "rebuild completed"

root_owned=$(find "$CACHE" ! -user "$BUILD_USER" -print 2>/dev/null | head -5)
[ -z "$root_owned" ] || fail "files in $CACHE are not owned by $BUILD_USER, so something
      privileged wrote there:
$root_owned"
pass "everything under $CACHE belongs to $BUILD_USER: the compiler was not root"

[ -d "$CACHE/tree/stmak" ] || fail "$CACHE/tree/stmak missing; the scratch copy must be named stmak
      so that a \"stmak/generated/...\" include resolves"
pass "scratch tree is named stmak"

# ---------------------------------------------------------------------------
step "3. the installed result"
# ---------------------------------------------------------------------------

server="$STATE/bin/stmakd"
[ -f "$server" ] || fail "$server missing"
[ ! -L "$server" ] || fail "$server is still a symlink; the rebuild did not replace it"
[ "$(stat -c '%U:%G' "$server")" = "root:root" ] || fail "$server is not root-owned"
pass "a real, root-owned binary replaced the symlink"

newcaps=$(getcap "$server" | sed 's/^[^ ]* //')
[ "$newcaps" = "$caps" ] || fail "capabilities were not carried across:
      packaged: $caps
      rebuilt:  $newcaps"
pass "capabilities carried across: $newcaps"

"$server" -h >/dev/null 2>&1 || fail "the rebuilt server does not run"
pass "the rebuilt server runs"

[ -f "$LIBEXEC" ] || fail "the packaged server was touched; it must never be"
pass "the packaged server is untouched"

# ---------------------------------------------------------------------------
step "4. add-gomod"
# ---------------------------------------------------------------------------

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mod="$tmp/verifymod"
mkdir -p "$mod"

cat > "$mod/go.mod" <<'EOF'
module verifymod

go 1.21
EOF

cat > "$mod/verifymod.go" <<'EOF'
// Package verifymod exists only to prove that an external module reaches the
// rebuilt server.
//
// The marker is printed from init() rather than left as a constant: an
// unreferenced Go const never reaches the binary at all, so grepping for one
// would prove nothing. Reaching init() proves both that the package was
// linked in and that the generated imports actually pull it in.
package verifymod

import "os"

func init() {
	if os.Getenv("STRATUMAK_VERIFY_MARKER") != "" {
		os.Stderr.WriteString("stratumak-verify-external-module\n")
	}
}
EOF

# Deliberately unreadable to anyone but root, which is the case that made
# staging necessary: the build identity must still end up able to compile it.
chmod 0700 "$mod"
chmod 0600 "$mod"/*

modcompile add-gomod "$mod"
pass "add-gomod completed"

[ -d "$STATE/modules/verifymod" ] || fail "$STATE/modules/verifymod missing; the module was not recorded"
[ "$(stat -c '%U' "$STATE/modules/verifymod")" = root ] || fail "the recorded copy is not root-owned"
pass "recorded root-owned in the registry"

if ! STRATUMAK_VERIFY_MARKER=1 "$server" -h 2>&1 | grep -q "stratumak-verify-external-module"; then
    fail "the rebuilt server does not run the module's init(); it was not compiled in"
fi
pass "the module is compiled into the server and its init() runs"

modcompile list | grep -q "external/verifymod" || fail "modcompile list does not report the module"
pass "modcompile list reports it"

# ---------------------------------------------------------------------------
step "5. modcompile --install"
# ---------------------------------------------------------------------------

comp="$tmp/verifycmod"
mkdir -p "$comp"
cat > "$comp/verify_helper.h" <<'EOF'
#define VERIFY_HELPER_MAGIC 4711
EOF
cat > "$comp/verifycmod.comp" <<'EOF'
component verifycmod "cmod used by the privileged-rebuild verification";
pin in bit ok;
pin out s32 value;
function _;
license "GPL";
;;
#include "verify_helper.h"
FUNCTION(_) { value = VERIFY_HELPER_MAGIC; }
EOF
chmod 0700 "$comp"
chmod 0600 "$comp"/*

modcompile --install "$comp/verifycmod.comp"
pass "--install completed (with a local header, from a 0700 directory)"

so="$STATE/cmod/verifycmod.so"
[ -f "$so" ] || fail "$so missing; --install did not put the module where the launcher looks"
[ "$(stat -c '%U:%G' "$so")" = "root:root" ] || fail "$so is not root-owned"
pass "$so installed root-owned"

nm -D --defined-only "$so" | grep -q cmod_abi_version \
    || fail "$so carries no cmod ABI stamp; the launcher will refuse it"
pass "it carries the cmod ABI stamp"

root_owned=$(find "$CACHE" ! -user "$BUILD_USER" -print 2>/dev/null | head -5)
[ -z "$root_owned" ] || fail "the cmod compile left root-owned files in $CACHE:
$root_owned"
pass "the cmod compiler was not root either"

rm -f "$so"

# ---------------------------------------------------------------------------
step "6. rm-gomod, and back to where we started"
# ---------------------------------------------------------------------------

modcompile rm-gomod verifymod
[ ! -d "$STATE/modules/verifymod" ] || fail "the registry entry survived rm-gomod"
if STRATUMAK_VERIFY_MARKER=1 "$server" -h 2>&1 | grep -q "stratumak-verify-external-module"; then
    fail "the module is still compiled into the server after rm-gomod"
fi
pass "module removed and compiled out again"

echo
echo "All checks passed."
echo
echo "The server at $server is now a local build with no external modules."
echo "To hand the machine back to the packaged binary:"
echo "    sudo rm -f $server"
echo "    sudo ln -s $LIBEXEC $server"
