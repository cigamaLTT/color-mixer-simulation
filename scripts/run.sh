#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ ! -x "$project_dir/.build/mixer-iot" ]]; then
  "$project_dir/scripts/build-host.sh"
fi
export WEB_DIR="${WEB_DIR:-$project_dir/iot/web}"
cd "$project_dir"
exec "$project_dir/.build/mixer-iot"
