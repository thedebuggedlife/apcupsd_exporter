#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 targets <current-version> <releases.json>" >&2
  echo "       $0 apply <version> <golang-digest> <distroless-digest> [dir]" >&2
  exit 2
}

version_re='^[0-9]+\.[0-9]+\.[0-9]+$'
digest_re='^sha256:[0-9a-f]{64}$'

targets() {
  [[ $# -eq 2 && $1 =~ $version_re ]] || usage
  local current=$1 releases=$2
  jq -r --arg current "$current" '
    def tuple: split(".") | map(tonumber);
    ($current | tuple) as $c
    | [.[]
        | select(.stable == true)
        | .version
        | select(test("^go[0-9]+\\.[0-9]+\\.[0-9]+$"))
        | ltrimstr("go")
        | {v: ., t: tuple}] as $all
    | ($all | map(select(.t[0:2] == $c[0:2] and .t > $c)) | max_by(.t)) as $patch
    | ($all | map(select(.t[0:2] > $c[0:2])) | max_by(.t)) as $minor
    | "patch=\($patch.v // "")", "minor=\($minor.v // "")"
  ' "$releases"
}

apply() {
  [[ $# -eq 3 || $# -eq 4 ]] || usage
  [[ $1 =~ $version_re && $2 =~ $digest_re && $3 =~ $digest_re ]] || usage
  local version=$1 golang_digest=$2 distroless_digest=$3 dir=${4:-.}
  local gomod=$dir/go.mod dockerfile=$dir/Dockerfile
  local golang_re='^FROM .*golang:[^ ]+'
  local distroless_re='^FROM gcr.io/distroless/static-debian13:nonroot@sha256:[0-9a-f]+'

  [[ -f $gomod ]] || { echo "$gomod not found" >&2; exit 1; }
  [[ -f $dockerfile ]] || { echo "$dockerfile not found" >&2; exit 1; }
  [[ $(grep -c '^go [0-9]' "$gomod" || true) -eq 1 ]] || { echo "$gomod needs exactly one go line" >&2; exit 1; }
  [[ $(grep -cE "$golang_re" "$dockerfile" || true) -eq 1 ]] || { echo "$dockerfile needs exactly one golang FROM line" >&2; exit 1; }
  [[ $(grep -cE "$distroless_re" "$dockerfile" || true) -eq 1 ]] || { echo "$dockerfile needs exactly one pinned distroless FROM line" >&2; exit 1; }

  gomod_tmp=$(mktemp "$dir/.go.mod.XXXXXX")
  dockerfile_tmp=$(mktemp "$dir/.Dockerfile.XXXXXX")
  trap 'rm -f "${gomod_tmp:-}" "${dockerfile_tmp:-}"' EXIT

  cp -p "$gomod" "$gomod_tmp"
  cp -p "$dockerfile" "$dockerfile_tmp"
  sed -E "s|^go [0-9].*|go $version|" "$gomod" >"$gomod_tmp"
  sed -E \
    -e "/$golang_re/s|golang:[^ ]+|golang:$version-trixie@$golang_digest|" \
    -e "s|^(FROM gcr.io/distroless/static-debian13:nonroot)@sha256:[0-9a-f]+|\\1@$distroless_digest|" \
    "$dockerfile" >"$dockerfile_tmp"
  mv "$gomod_tmp" "$gomod"
  mv "$dockerfile_tmp" "$dockerfile"
}

[[ $# -ge 1 ]] || usage
command=$1
shift
case $command in
  targets) targets "$@" ;;
  apply) apply "$@" ;;
  *) usage ;;
esac
