#!/bin/bash
# rt-clang.sh — provide the pinned clang used by the RT function-effects
# check (make rt-effects-check).
#
# clang's -Wfunction-effects analysis needs clang >= 20 (the clang 19
# implementation is broken: false positives on correct conversions, no
# body verification).  Debian 13 ships clang 19, so the check uses a
# pinned official LLVM release binary, downloaded once into the user
# cache and verified against the sha256 digests published by the LLVM
# project on GitHub.  Only the compiler frontend and its resource
# headers are extracted (~370 MB); the tarball is deleted after
# extraction.
#
# This toolchain is DIAGNOSTIC ONLY: it never compiles code into any
# build artifact.  gcc (or the configured compiler) remains the
# production toolchain, and the tree builds fully without this script.
#
# Prints the path of the clang binary on stdout; everything else goes to
# stderr.  Exits nonzero if the host arch is unsupported or the download
# cannot be verified.
#
# Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
# License: GPL Version 2

set -eu

LLVM_VERSION="22.1.8"

case "$(uname -m)" in
    x86_64)
        LLVM_ARCH="X64"
        LLVM_SHA256="df0e1ecf16caf3489a272a5eea4eec9b0d82878f6477fa309504f918a0006384"
        ;;
    aarch64)
        LLVM_ARCH="ARM64"
        LLVM_SHA256="805efad2bb91cb4967fa569e0881d10c0f69c04461cf671cccbae19f547acc34"
        ;;
    *)
        echo "rt-clang.sh: unsupported architecture: $(uname -m)" >&2
        echo "rt-clang.sh: the RT effects check is only available on x86_64/aarch64" >&2
        exit 2
        ;;
esac

LLVM_DIR="LLVM-${LLVM_VERSION}-Linux-${LLVM_ARCH}"
LLVM_TAR="${LLVM_DIR}.tar.xz"
LLVM_URL="https://github.com/llvm/llvm-project/releases/download/llvmorg-${LLVM_VERSION}/${LLVM_TAR}"

CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/linuxcnc-rt-clang"
CLANG="${CACHE_DIR}/${LLVM_DIR}/bin/clang"

if [ -x "$CLANG" ]; then
    echo "$CLANG"
    exit 0
fi

echo "rt-clang.sh: downloading pinned clang ${LLVM_VERSION} (${LLVM_ARCH})..." >&2
echo "rt-clang.sh: ${LLVM_URL}" >&2

mkdir -p "$CACHE_DIR"
TMP_TAR="${CACHE_DIR}/${LLVM_TAR}.part"
trap 'rm -f "$TMP_TAR"' EXIT

curl -fsSL -o "$TMP_TAR" -C - "$LLVM_URL" || curl -fsSL -o "$TMP_TAR" "$LLVM_URL"

echo "rt-clang.sh: verifying sha256..." >&2
echo "${LLVM_SHA256}  ${TMP_TAR}" | sha256sum -c - >&2

echo "rt-clang.sh: extracting compiler frontend..." >&2
tar -xJf "$TMP_TAR" -C "$CACHE_DIR" \
    "${LLVM_DIR}/bin/clang" \
    "${LLVM_DIR}/bin/clang++" \
    "${LLVM_DIR}/bin/clang-${LLVM_VERSION%%.*}" \
    "${LLVM_DIR}/lib/clang"
rm -f "$TMP_TAR"

if [ ! -x "$CLANG" ]; then
    echo "rt-clang.sh: extraction failed" >&2
    exit 1
fi

echo "$CLANG"
