#!/bin/bash
# sync-oracle.sh — vendor the LinuxCNC 2.9.8 motion-logger gold files into
# oracle-2.9/ so the harness runs self-contained in this repo, and write a
# provenance MANIFEST. This is the ONLY script that reads the 2.9 tree.
#
#   LCNC29=/path/to/linuxcnc-2.9 ./sync-oracle.sh   # default ~/source/linuxcnc-2.9
#
# Re-run to refresh; `git diff oracle-2.9/` then shows whether the 2.9 oracle
# drifted (e.g. after re-capturing against a newer 2.9.8 milltask).
set -eu
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LCNC29="${LCNC29:-$HOME/source/linuxcnc-2.9}"
SRC="$LCNC29/tests"
DEST="$HERE/oracle-2.9"
# shellcheck source=targets.sh
source "$HERE/targets.sh"

[ -d "$SRC" ] || { echo "2.9 tree not found: $SRC  (set LCNC29=)"; exit 2; }

n=0
for row in "${PARITY_TARGETS[@]}"; do
  # shellcheck disable=SC2086
  set -- $row
  label="$1"; oracle_rel="$2"; src_rel="$4"
  s="$SRC/$src_rel"; d="$DEST/$oracle_rel"
  if [ ! -f "$s" ]; then echo "MISSING in 2.9, skipped: $src_rel"; continue; fi
  mkdir -p "$(dirname "$d")"
  cp "$s" "$d"
  n=$((n + 1))
  printf '  vendored %-12s <- tests/%s\n' "$label" "$src_rel"
done

commit="$(git -C "$LCNC29" rev-parse HEAD 2>/dev/null || echo unknown)"
short="$( git -C "$LCNC29" rev-parse --short HEAD 2>/dev/null || echo unknown)"
branch="$(git -C "$LCNC29" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
cdate="$( git -C "$LCNC29" log -1 --format=%ci 2>/dev/null || echo unknown)"
{
  echo "# Vendored LinuxCNC 2.9 motion-logger oracle — provenance."
  echo "# Regenerate with ./sync-oracle.sh (reads \$LCNC29). Do not hand-edit the golds."
  echo "source_tree:   $LCNC29"
  echo "source_commit: $commit"
  echo "source_short:  $short"
  echo "source_branch: $branch"
  echo "source_date:   $cdate"
  echo "synced_at:     $(date '+%Y-%m-%d %H:%M:%S %z')"
  echo "files:         $n"
} > "$DEST/MANIFEST"
echo "wrote $DEST/MANIFEST  ($n files, 2.9 @ $short on $branch)"
