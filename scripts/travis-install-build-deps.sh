#!/bin/sh -ex
# Installs the build-deps for a RIP build. Runs unattended in CI, on the
# GitHub runner and inside the CI image (.github/ci/Dockerfile), where it is
# root and has no use for sudo.
#
# Everything is passed through `env` because sudo resets the environment:
# exported variables would never reach apt. needrestart ignores
# DEBIAN_FRONTEND, and after build-dep upgrades the systemd stack on the runner
# it can sit on a prompt nobody will answer; NEEDRESTART_* keep it quiet.
# The Acquire options bound a stalled mirror instead of letting apt retry for
# the rest of the job.
SUDO=sudo
[ "$(id -u)" -ne 0 ] || SUDO=
APT="$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 \
    apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 \
    -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"

$APT update -qq
$APT install -y devscripts equivs build-essential --no-install-recommends
$APT remove -f libreadline6-dev || true
$APT remove -f libreadline-dev || true
debian/configure
# This gives an error that is hidden from us
#mk-build-deps -i -r -s sudo -t 'apt-get -o Debug::pkgProblemResolver=yes --no-install-recommends --no-install-suggests'
if ! $APT -y build-dep . ; then
    echo "E: could not install dependencies, maybe inspect d/control for hints:"
    cat debian/control
    echo
    echo "E: apt-get -y build-dep . failed"
    exit 1
fi
$APT install -y lintian
