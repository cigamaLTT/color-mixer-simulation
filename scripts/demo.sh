#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${MQTT_URL:-}" && -z "${VIRTUAL_BROKER_ADDR:-}" ]]; then
  printf '%s\n' 'Set VIRTUAL_BROKER_ADDR=host:port to the same external broker as MQTT_URL.' >&2
  exit 2
fi
"$project_dir/scripts/build-host.sh"
export WEB_DIR="${WEB_DIR:-$project_dir/iot/web}"
backend_pid=''
device_pid=''
cleanup() {
  trap - EXIT INT TERM
  [[ -z "$device_pid" ]] || kill "$device_pid" 2>/dev/null || true
  [[ -z "$backend_pid" ]] || kill "$backend_pid" 2>/dev/null || true
  [[ -z "$device_pid" ]] || wait "$device_pid" 2>/dev/null || true
  [[ -z "$backend_pid" ]] || wait "$backend_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$project_dir"
"$project_dir/.build/mixer-iot" &
backend_pid=$!
http_address="${HTTP_ADDR:-127.0.0.1:8080}"
http_address="${http_address/0.0.0.0/127.0.0.1}"
http_address="${http_address/\[::\]/127.0.0.1}"
for attempt in {1..100}; do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    wait "$backend_pid"
    exit 1
  fi
  if curl --silent --fail "http://$http_address/healthz" >/dev/null; then
    break
  fi
  sleep 0.1
done
curl --silent --fail "http://$http_address/healthz" >/dev/null
broker_address="${VIRTUAL_BROKER_ADDR:-${MQTT_ADDR:-127.0.0.1:1883}}"
broker_address="${broker_address/0.0.0.0/127.0.0.1}"
broker_address="${broker_address/\[::\]/[::1]}"
"$project_dir/firmware/target/release/virtual-device" \
  --broker "$broker_address" \
  --device-id "${DEVICE_ID:-mixer-01}" \
  --speed "${SIM_SPEED:-1}" &
device_pid=$!
printf '\nNative demo: http://%s\n' "$http_address"
printf '%s\n' 'Device runs the shared Rust controller with a software plant. This is not Wokwi.' 'Ctrl+C stops both processes.'
wait -n "$backend_pid" "$device_pid"
