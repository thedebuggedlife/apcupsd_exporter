#!/usr/bin/env bash
# Remove lumen indexes whose project directory no longer exists (folded worktrees, temp dirs).
#   lumen-prune.sh [--dry-run] [path...]   -> each path's index is removed even if the path still exists
set -u

DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/lumen"
STALE_DAYS=7

dry_run=0
doomed=()
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    *) doomed+=("${arg%/}") ;;
  esac
done

is_doomed() {
  local p
  for p in ${doomed[@]+"${doomed[@]}"}; do
    [ "$1" = "$p" ] && return 0
  done
  return 1
}

[ -d "$DATA_DIR" ] || exit 0

count=0
for dir in "$DATA_DIR"/*/; do
  db="${dir}index.db"
  [ -f "$db" ] || continue
  # immutable=1: a WAL-mode db without its -shm file cannot be opened plain read-only.
  path=$(sqlite3 "file:${db}?immutable=1" "select value from project_meta where key='project_path'" 2>/dev/null) || continue
  if [ -z "$path" ]; then
    # No recorded path = an indexing run that died before writing its metadata.
    [ -n "$(find "$db" -mtime +"$STALE_DAYS")" ] || continue
    reason="no project_path, untouched for ${STALE_DAYS}+ days"
  elif is_doomed "$path"; then
    reason="$path (requested)"
  elif [ ! -d "$path" ]; then
    reason="$path (gone)"
  else
    continue
  fi
  size=$(du -sh "$dir" | cut -f1)
  if [ "$dry_run" = 1 ]; then
    echo "[lumen-prune] would remove $(basename "$dir") $size — $reason"
  else
    rm -rf "$dir"
    echo "[lumen-prune] removed $(basename "$dir") $size — $reason"
  fi
  count=$((count + 1))
done
echo "[lumen-prune] $count index(es) $([ "$dry_run" = 1 ] && echo "would be removed" || echo removed)"
