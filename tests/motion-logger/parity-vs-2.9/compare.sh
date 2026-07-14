#!/bin/bash
# compare.sh [target ...] — diff the gomc motion-logger gold against the vendored
# LinuxCNC 2.9.8 oracle (oracle-2.9/, populated by ./sync-oracle.sh) through the
# shared normalizer. Self-contained: needs no 2.9 tree checked out.
#
# Targets are labels from targets.sh (basic/g0, basic/g1, basic/s, mountaindew,
# m98m99-12); the prefix `basic` selects all three basic segments. No argument
# runs every target.
#
#   ./compare.sh                 # all
#   ./compare.sh basic           # basic/g0 basic/g1 basic/s
#   ./compare.sh mountaindew m98m99-12
#   ./compare.sh --self          # determinism check: gomc gold vs itself (must be PARITY)
#
# Exit 0 = every requested target reached parity; 1 = at least one diverged.
# Surviving diffs are REAL milltask behaviour to adjudicate — see PARITY_FINDINGS.md.
# Machine bring-up is excluded (preamble stripped / builtin-startup not vendored).
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORACLE="$HERE/oracle-2.9"
# shellcheck source=targets.sh
source "$HERE/targets.sh"

SELF=0
if [ "${1:-}" = "--self" ]; then SELF=1; shift; fi

# Provenance banner so every run states what it certifies against.
if [ -f "$ORACLE/MANIFEST" ]; then
  grep -E '^source_(short|branch|date):' "$ORACLE/MANIFEST" | sed 's/^/# oracle /'
else
  echo "# no vendored oracle — run ./sync-oracle.sh first" >&2
fi

want() {  # is label $1 requested by the (possibly empty) filter list?
  [ ${#FILTER[@]} -eq 0 ] && return 0
  local t
  for t in "${FILTER[@]}"; do
    [ "$t" = "$1" ] && return 0
    case "$1" in "$t"/*) return 0;; esac   # prefix match, e.g. basic -> basic/g0
  done
  return 1
}

FILTER=("$@")
rc=0
ran=0
for row in "${PARITY_TARGETS[@]}"; do
  # shellcheck disable=SC2086
  set -- $row
  label="$1"; oracle_rel="$2"; gomc_rel="$3"; strip="$5"; units="$6"
  want "$label" || continue
  ran=$((ran + 1))
  [ "$strip" = "strip" ] && strip="--strip-preamble" || strip=""

  new="$HERE/$gomc_rel"
  if [ "$SELF" = 1 ]; then old="$new" old_units=1; else old="$ORACLE/$oracle_rel" old_units="$units"; fi

  if [ ! -f "$old" ]; then printf 'SKIP        %-14s (missing oracle: %s)\n' "$label" "$oracle_rel"; continue; fi
  if [ ! -f "$new" ]; then printf 'SKIP        %-14s (missing gomc gold: %s)\n' "$label" "$gomc_rel"; continue; fi

  na="$(mktemp)"; nb="$(mktemp)"
  # Oracle side: scale machine-unit lengths to mm; gomc side is mm already.
  "$HERE/normalize.sh" $strip --units-factor "$old_units" "$old" > "$na"
  "$HERE/normalize.sh" $strip --units-factor 1 "$new" > "$nb"
  if diff -q "$na" "$nb" >/dev/null; then
    printf 'PARITY:     %-14s (%s motion commands)\n' "$label" "$(wc -l < "$na")"
  else
    printf 'DIVERGENCE: %-14s (< 2.9 milltask   > gomc milltask)\n' "$label"
    diff --label "2.9/$label" --label "gomc/$label" -u "$na" "$nb" | sed 's/^/    /'
    rc=1
  fi
  rm -f "$na" "$nb"
done

[ "$ran" = 0 ] && { echo "no targets matched: ${FILTER[*]}" >&2; exit 2; }
exit $rc
