#!/usr/bin/env bash
# Tests scripts/go-toolchain.sh. Usage: scripts/test-go-toolchain.sh
set -euo pipefail

if [[ $# -ne 0 ]]; then
  echo "usage: $0" >&2
  exit 2
fi

here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
script=$here/go-toolchain.sh

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  echo "FAIL: $1: $2" >&2
  exit 1
}

# Each argument is a version, optionally suffixed with :unstable.
releases_json() {
  local out="" sep="" entry version stable
  for entry in "$@"; do
    version=${entry%%:*}
    stable=true
    if [[ $entry == *:unstable ]]; then
      stable=false
    fi
    out="$out$sep{\"version\":\"go$version\",\"stable\":$stable,\"files\":[]}"
    sep=,
  done
  echo "[$out]"
}

expect_targets() {
  local name=$1 current=$2 want_patch=$3 want_minor=$4
  shift 4
  local file=$work/releases.json got want
  releases_json "$@" >"$file"
  want=$(printf 'patch=%s\nminor=%s' "$want_patch" "$want_minor")
  got=$("$script" targets "$current" "$file") || fail "$name" "exited $?"
  [[ $got == "$want" ]] || fail "$name" "want $(tr '\n' ' ' <<<"$want"), got $(tr '\n' ' ' <<<"$got")"
}

expect_targets "no newer release" 1.27.3 "" "" 1.27.3 1.26.10
expect_targets "patch only" 1.27.3 1.27.4 "" 1.27.4 1.26.11
expect_targets "patch and minor" 1.27.3 1.27.4 1.28.0 1.28.0 1.27.4
expect_targets "minor only" 1.27.3 "" 1.28.1 1.28.1 1.27.3
expect_targets "older minor line is not a patch" 1.26.10 "" 1.28.1 1.28.1 1.27.5
expect_targets "numeric patch compare" 1.27.9 1.27.10 "" 1.27.10 1.26.20
expect_targets "numeric patch compare among candidates" 1.27.3 1.27.10 "" 1.27.9 1.27.10 1.27.4
expect_targets "unstable is ignored" 1.27.3 "" "" 1.28rc1:unstable 1.27.3
expect_targets "unstable numeric release is ignored" 1.27.3 1.27.4 1.28.0 1.27.5:unstable 1.28.2:unstable 1.27.4 1.28.0
expect_targets "older patch is ignored" 1.27.3 "" "" 1.27.2 1.26.10
expect_targets "newest of several minors" 1.27.3 "" 1.29.0 1.28.0 1.29.0 1.28.5
expect_targets "numeric minor compare" 1.9.3 "" 1.10.0 1.10.0 1.9.3
expect_targets "numeric minor compare among candidates" 1.8.0 "" 1.10.0 1.9.3 1.10.0
expect_targets "release candidate marked stable is ignored" 1.27.3 "" "" 1.28rc1 1.27.3

current=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$repo/go.mod")
[[ $current =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail setup "go.mod has no X.Y.Z go line"
next=${current%.*}.$((${current##*.} + 1))

a=sha256:$(printf 'a%.0s' $(seq 64))
b=sha256:$(printf 'b%.0s' $(seq 64))

new_dir() {
  local dir=$work/$1
  mkdir "$dir"
  cp "$repo/Dockerfile" "$repo/go.mod" "$dir/"
  echo "$dir"
}

entries() {
  find "$1" -mindepth 1 | wc -l | tr -d ' '
}

expect_exit() {
  local name=$1 want=$2 got=0
  shift 2
  "$@" >/dev/null 2>&1 || got=$?
  [[ $got -eq $want ]] || fail "$name" "want exit $want, got $got"
}

dir=$(new_dir apply)
"$script" apply "$next" "$a" "$b" "$dir" || fail apply "exited $?"

want_golang="FROM --platform=\$BUILDPLATFORM golang:$next-trixie@$a AS build"
want_distroless="FROM gcr.io/distroless/static-debian13:nonroot@$b"
[[ $(sed -n 2p "$dir/Dockerfile") == "$want_golang" ]] || fail apply "Dockerfile line 2 is $(sed -n 2p "$dir/Dockerfile")"
grep -qxF "$want_distroless" "$dir/Dockerfile" || fail apply "distroless line missing"
[[ $(grep -c '^go ' "$dir/go.mod") -eq 1 ]] || fail apply "go.mod has no single go line"
grep -qxF "go $next" "$dir/go.mod" || fail apply "go.mod lacks go $next"

changed=$(diff "$repo/go.mod" "$dir/go.mod" | grep -c '^>' || true)
[[ $changed -eq 1 ]] || fail apply "go.mod changed $changed lines"
changed=$(diff "$repo/Dockerfile" "$dir/Dockerfile" | grep -c '^>' || true)
[[ $changed -eq 2 ]] || fail apply "Dockerfile changed $changed lines"
removed=$(diff "$repo/Dockerfile" "$dir/Dockerfile" | grep -c '^<' || true)
[[ $removed -eq 2 ]] || fail apply "Dockerfile removed $removed lines"

[[ $(entries "$dir") -eq 2 ]] || fail apply "left stray files"

cp "$dir/Dockerfile" "$work/applied.Dockerfile"
cp "$dir/go.mod" "$work/applied.go.mod"
"$script" apply "$next" "$a" "$b" "$dir" || fail "apply twice" "exited $?"
cmp -s "$dir/Dockerfile" "$work/applied.Dockerfile" || fail "apply twice" "Dockerfile changed"
cmp -s "$dir/go.mod" "$work/applied.go.mod" || fail "apply twice" "go.mod changed"

dir=$(new_dir no-golang)
grep -v 'golang:' "$repo/Dockerfile" >"$dir/Dockerfile"
expect_exit "apply without golang line" 1 "$script" apply "$next" "$a" "$b" "$dir"
cmp -s "$repo/go.mod" "$dir/go.mod" || fail "apply without golang line" "go.mod changed"
[[ $(entries "$dir") -eq 2 ]] || fail "apply without golang line" "left stray files"

dir=$(new_dir no-distroless)
grep -v 'distroless' "$repo/Dockerfile" >"$dir/Dockerfile"
expect_exit "apply without distroless line" 1 "$script" apply "$next" "$a" "$b" "$dir"
cmp -s "$repo/go.mod" "$dir/go.mod" || fail "apply without distroless line" "go.mod changed"

dir=$(new_dir no-gomod)
rm "$dir/go.mod"
expect_exit "apply without go.mod" 1 "$script" apply "$next" "$a" "$b" "$dir"
cmp -s "$repo/Dockerfile" "$dir/Dockerfile" || fail "apply without go.mod" "Dockerfile changed"

dir=$(new_dir no-go-line)
grep -v '^go ' "$repo/go.mod" >"$dir/go.mod"
expect_exit "apply without go line" 1 "$script" apply "$next" "$a" "$b" "$dir"
cmp -s "$repo/Dockerfile" "$dir/Dockerfile" || fail "apply without go line" "Dockerfile changed"

expect_exit "no arguments" 2 "$script"
expect_exit "unknown command" 2 "$script" frobnicate
expect_exit "targets with too few arguments" 2 "$script" targets 1.27.3
expect_exit "targets with a bad version" 2 "$script" targets v1.27 "$work/releases.json"
expect_exit "apply with too few arguments" 2 "$script" apply "$next" "$a"
expect_exit "apply with a bad version" 2 "$script" apply 1.27 "$a" "$b" "$work"
expect_exit "apply with a short digest" 2 "$script" apply "$next" sha256:abc "$b" "$work"

echo PASS
