#!/bin/bash
# normalize.sh <capture> — canonicalize a motion-logger capture (either the
# classic 2.9 motion-logger.c dialect or the gomc interceptor-cmod dialect) to
# the common canonical motion stream, for diffing. Writes to stdout.
#
#   1. canonicalize.awk : keep only the behavioural motion opcodes, strip
#      cross-tree-meaningless fields, and (for the 2.9 oracle side) scale
#      length-dimensioned fields machine-units->mm via --units-factor
#      (see that file for the full policy).
#   2. round every float to 5 SIGNIFICANT digits (after squashing |v|<1e-9 FP
#      dust to 0) so last-ULP noise does not mask real divergence. Significant
#      (not absolute-decimal) rounding because the logs carry 6 significant
#      digits (%.6g): after a x25.4 unit scaling a value like 10559.8 has no
#      trustworthy 4th decimal. %.5g is the tolerance knob.
#
# Pass --strip-preamble for a combined capture that inlines the machine bring-up
# before the program (e.g. mountaindew's single expected.motion-logger); omit it
# for pre-split program segments (e.g. basic's expected.g1). See canonicalize.awk.
# Pass --units-factor 25.4 for a capture in inch machine units (the 2.9 oracle
# on the current corpus); gomc captures are mm already (factor 1).
set -u
# Numeric parsing/printing must be locale-independent (decimal POINT): awk's
# str->num coercion and sprintf are locale-sensitive.
export LC_ALL=C
STRIP=0
FACTOR=1
while :; do
  case "${1:-}" in
    --strip-preamble) STRIP=1; shift;;
    --units-factor)   FACTOR="${2:?--units-factor needs a value}"; shift 2;;
    *) break;;
  esac
done
IN="${1:?usage: normalize.sh [--strip-preamble] [--units-factor F] <capture>}"
DIR="$(cd "$(dirname "$0")" && pwd)"
awk -v strip_preamble="$STRIP" -v unit_factor="$FACTOR" \
    -f "$DIR/canonicalize.awk" "$IN" \
  | perl -pe 's/(-?\d+\.\d+(?:e[+-]?\d+)?|-?\d+e[+-]?\d+)/sprintf("%.5g", abs($1) < 1e-9 ? 0 : $1)/gei'
