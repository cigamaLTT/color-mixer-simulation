#!/usr/bin/env bash
set -euo pipefail
firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$firmware_dir/tools"
if ! command -v rustup >/dev/null; then
  printf '%s\n' 'Install rustup first: https://rustup.rs' >&2
  exit 1
fi
if [[ ! -x "$firmware_dir/tools/espup" ]]; then
  case "$(uname -m)" in
    x86_64) espup_host=x86_64-unknown-linux-gnu ;;
    aarch64) espup_host=aarch64-unknown-linux-gnu ;;
    *) printf '%s\n' 'Install espup for your host: https://github.com/esp-rs/espup' >&2; exit 1 ;;
  esac
  curl --fail --location --retry 2 \
    "https://github.com/esp-rs/espup/releases/download/v0.19.0/espup-$espup_host" \
    --output "$firmware_dir/tools/espup"
  chmod +x "$firmware_dir/tools/espup"
fi
"$firmware_dir/tools/espup" install --targets esp32 --toolchain-version 1.86.0 \
  --export-file "$firmware_dir/tools/export-esp.sh"
