#!/bin/bash
# normalize.sh <capture> — canonicalize a motion-logger capture (either the
# classic 2.9 motion-logger.c dialect or the gomc interceptor-cmod dialect) to
# the common canonical motion stream, for diffing. Writes to stdout.
#
#   1. canonicalize.awk : keep only the behavioural motion opcodes and strip
#      cross-tree-meaningless fields (see that file for the full policy).
#   2. round every float to 4 decimals so last-ULP FP noise does not mask real
#      divergence. %.4f is the tolerance knob (kept identical to
#      tests/milltask-parity/round.sh on purpose).
#
# Pass --strip-preamble for a combined capture that inlines the machine bring-up
# before the program (e.g. mountaindew's single expected.motion-logger); omit it
# for pre-split program segments (e.g. basic's expected.g1). See canonicalize.awk.
set -u
STRIP=0
if [ "${1:-}" = "--strip-preamble" ]; then STRIP=1; shift; fi
IN="${1:?usage: normalize.sh [--strip-preamble] <capture>}"
DIR="$(cd "$(dirname "$0")" && pwd)"
awk -v strip_preamble="$STRIP" -f "$DIR/canonicalize.awk" "$IN" \
  | perl -pe 's/(-?\d+\.\d+(?:e[+-]?\d+)?)/sprintf("%.4f",$1)/ge'
