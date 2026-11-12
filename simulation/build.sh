#!/usr/bin/env bash
set -euo pipefail
simulation_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$simulation_dir/build"
"${CLANG:-clang}" --target=wasm32-unknown-unknown -std=c11 -Oz \
  -Wall -Wextra -Werror -ffreestanding -fno-builtin -nostdlib \
  -isystem "$simulation_dir/include" -I "$simulation_dir" \
  -Wl,--no-entry -Wl,--import-memory -Wl,--export-table \
  -Wl,-z,stack-size=65536 -Wl,--initial-memory=2097152 \
  "$simulation_dir/mixer.chip.c" "$simulation_dir/mixer_model.c" \
  "$simulation_dir/hx711_model.c" "$simulation_dir/mixer_render.c" \
  -o "$simulation_dir/build/mixer.chip.wasm"
cp "$simulation_dir/mixer.chip.json" "$simulation_dir/build/mixer.chip.json"
printf 'Built %s\n' "$simulation_dir/build/mixer.chip.wasm"
