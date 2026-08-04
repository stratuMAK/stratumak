#!/bin/bash
# The package gate, as runnable steps. CI (package-gate in ci.yml) and local
# runs call the SAME stages so the two cannot drift, and a failure is fixed
# and re-verified per stage locally before anything is pushed.
#
# Usage: package-gate.sh <stage>... | all
#   build     dpkg-buildpackage -us -uc -b   (run from the source tree root)
#   contents  scripts/check-deb-contents.sh over ../
#   lintian   lintian --fail-on error,warning over the .changes
#   install   apt-get install the built debs           (needs root)
#   smoke     run debian/tests/stmak-test as an unprivileged user
#   extcomp   modcompile --install a scratch .comp     (needs root)
#
# Local iteration: after a full `build`, packaging-metadata changes re-verify
# in minutes with `dpkg-buildpackage -nc` before re-running later stages.
set -e

stage_build() {
    dpkg-buildpackage -us -uc -b
}

stage_contents() {
    bash scripts/check-deb-contents.sh ..
}

stage_lintian() {
    lintian --fail-on error,warning --no-tag-display-limit ../stratumak_*.changes
}

stage_install() {
    apt-get --yes --quiet install ../*.deb
}

stage_smoke() {
    # The testrunner user exists only to drop root (the CI container runs the
    # gate as root, and linuxcnc must not). An unprivileged caller runs as-is.
    if [ "$(id -u)" = 0 ]; then
        id testrunner >/dev/null 2>&1 || adduser --disabled-password --gecos "" testrunner
        chmod 0777 debian/tests
        su -c "cd debian/tests && ./stmak-test" testrunner
    else
        (cd debian/tests && ./stmak-test)
    fi
}

stage_extcomp() {
    tmp=$(mktemp -d)
    printf 'component cigate "gate scratch component";\npin in bit a;\npin out bit out;\nfunction _;\nlicense "GPL";\n;;\nFUNCTION(_) { out = a; }\n' > "$tmp/cigate.comp"
    modcompile --install "$tmp/cigate.comp"
    test -f /var/lib/stratumak/cmod/cigate.so
    rm -rf "$tmp"
}

[ $# -ge 1 ] || { grep '^#   ' "$0"; exit 2; }
stages="$*"
[ "$stages" = "all" ] && stages="build contents lintian install smoke extcomp"
for s in $stages; do
    echo "=== gate stage: $s"
    "stage_$s"
    echo "=== gate stage: $s OK"
done
