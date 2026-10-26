# CI build image: debian:trixie with the package build-deps and the CI-only
# tools preinstalled, so the RIP jobs stop spending minutes (and a systemd
# upgrade on a live runner) on apt before every build.
#
# Built and pushed by the ci-image job in .github/workflows/ci.yml. The build
# context is debian/ alone: the build-deps are all this image derives from the
# tree. The tag hashes this file plus the inputs of debian/configure, so a
# change to either produces a new image, and carries the ISO week, so the
# image never drifts more than a week behind the archive.
#
# The jobs still run scripts/travis-install-build-deps.sh on top of this
# image. Normally that is a no-op of a few seconds; when a PR adds a
# build-dep, it installs just that package, so nobody waits for an image
# rebuild before their PR can go green.
FROM debian:trixie

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    GOTOOLCHAIN=local

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates eatmydata git lsb-release python3 sudo \
        devscripts equivs build-essential \
 && rm -rf /var/lib/apt/lists/*

COPY . /tmp/deps/debian/
RUN cd /tmp/deps \
 && debian/configure \
 && apt-get update \
 && eatmydata apt-get install -y --no-install-recommends \
        ccache clang lcov lintian cppcheck shellcheck \
 && eatmydata apt-get build-dep -y . \
 && rm -rf /tmp/deps /var/lib/apt/lists/*

# Keep in sync with GOLANGCI_LINT_VERSION in src/stmak/Submakefile.
# GOTOOLCHAIN=auto for this one install: golangci-lint's own module needs a
# newer Go than the one the project pins, and the project build must never
# fetch a toolchain, so the image default stays local.
ARG GOLANGCI_LINT_VERSION=v2.12.2
RUN GOTOOLCHAIN=auto GOBIN=/usr/local/bin GOPATH=/tmp/gopath GOCACHE=/tmp/gocache \
        go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
 && rm -rf /tmp/gopath /tmp/gocache

# actions/checkout runs as root in the container on a workspace bind-mounted
# from the runner, and git refuses to touch a repo owned by someone else.
RUN git config --system --add safe.directory '*'
