#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -z "${WOKWI_CLI_TOKEN:-}" ]]; then
  printf '%s\n' 'WOKWI_CLI_TOKEN is required by Wokwi CLI. See docs/wokwi.md.' >&2
  exit 2
fi
wokwi_command="${WOKWI_CLI:-wokwi-cli}"
if ! command -v "$wokwi_command" >/dev/null 2>&1; then
  if [[ -x "$project_dir/.tools/wokwi-cli" ]]; then
    wokwi_command="$project_dir/.tools/wokwi-cli"
  else
    printf '%s\n' 'Install Wokwi CLI or set WOKWI_CLI to its executable path. See docs/wokwi.md.' >&2
    exit 2
  fi
fi
cd "$project_dir"
exec "$wokwi_command" . "$@"
