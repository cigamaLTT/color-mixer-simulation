#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$project_dir/.build"
(
  cd "$project_dir/iot"
  go build -o "$project_dir/.build/mixer-iot" .
)
cargo build --release --manifest-path "$project_dir/firmware/Cargo.toml" -p virtual-device
printf 'Host builds ready: %s\n' "$project_dir/.build/mixer-iot"
