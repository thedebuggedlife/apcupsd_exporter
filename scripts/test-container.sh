#!/usr/bin/env bash
# Tests an image's own HEALTHCHECK. Usage: scripts/test-container.sh <image>
set -euo pipefail

if [[ $# -ne 1 || -z $1 ]]; then
  echo "usage: $0 <image>" >&2
  exit 2
fi

image=$1
run_id="$$-$RANDOM"
healthy="apcupsd-exporter-test-healthy-$run_id"
mismatched="apcupsd-exporter-test-mismatched-$run_id"
wait_seconds=60

cleanup() {
  docker rm --force "$healthy" "$mismatched" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  local name=$1 message=$2
  echo "FAIL: $name: $message" >&2
  docker inspect --format '{{json .State.Health}}' "$name" >&2 || true
  docker logs "$name" >&2 || true
  exit 1
}

# Publishes to a port Docker picks. Only timings change, so the image's health command still runs.
run_container() {
  local name=$1 port=$2
  shift 2
  docker run --detach --name "$name" --publish "127.0.0.1::$port" \
    --health-interval=1s --health-start-period=1s --health-retries=3 \
    "$image" "$@" >/dev/null
}

wait_for_health() {
  local name=$1 want=$2 deadline=$((SECONDS + wait_seconds)) health status
  while ((SECONDS < deadline)); do
    health=$(docker inspect --format '{{json .State.Health}}' "$name")
    if [[ $health == null ]]; then
      fail "$name" "the image has no HEALTHCHECK"
    fi

    status=$(grep -o '"Status":"[a-z]*"' <<<"$health" | head -n 1 | cut -d'"' -f4)
    case $status in
      "$want")
        echo "$name: $health"
        return 0
        ;;
      starting)
        sleep 1
        ;;
      *)
        fail "$name" "expected health $want, got ${status:-no status}"
        ;;
    esac
  done
  fail "$name" "no health result after ${wait_seconds}s, expected $want"
}

assert_running() {
  local name=$1
  if [[ $(docker inspect --format '{{.State.Running}}' "$name") != true ]]; then
    fail "$name" "the exporter is not running, so its health status proves nothing"
  fi
}

# Checks a response from outside the container. A connection failure has status 000.
assert_response() {
  local name=$1 port=$2 path=$3 want_status=$4 want_body=${5:-} address response status
  address=$(docker port "$name" "$port/tcp" | head -n 1)
  response=$(curl --silent --max-time 10 --write-out '\n%{http_code}' "http://$address$path" || true)
  status=${response##*$'\n'}

  if [[ $status != "$want_status" ]]; then
    fail "$name" "GET $path returned $status, expected $want_status: $response"
  fi
  if [[ $response != *"$want_body"* ]]; then
    fail "$name" "GET $path did not mention \"$want_body\": $response"
  fi
  echo "$name: GET $path returned $status"
}

echo "--- apcupsd unreachable: healthy, while /metrics reports it as down"
run_container "$healthy" 9162
wait_for_health "$healthy" healthy
assert_running "$healthy"
assert_response "$healthy" 9162 /healthz 200
assert_response "$healthy" 9162 /metrics 200 "apcupsd_up 0"

echo "--- mismatched -telemetry.addr: unhealthy, while the exporter serves"
run_container "$mismatched" 9163 -telemetry.addr=:9163
wait_for_health "$mismatched" unhealthy
assert_running "$mismatched"
assert_response "$mismatched" 9163 /healthz 200

echo "PASS: $image"
