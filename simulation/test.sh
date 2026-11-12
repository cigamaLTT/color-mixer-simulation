#!/usr/bin/env bash
set -euo pipefail
simulation_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
"$simulation_dir/build.sh"
"${CC:-cc}" -std=c11 -O2 -Wall -Wextra -Werror \
  -I "$simulation_dir" "$simulation_dir/tests/model_test.c" \
  "$simulation_dir/mixer_model.c" "$simulation_dir/hx711_model.c" \
  -o "$simulation_dir/build/model_test"
"$simulation_dir/build/model_test"
node "$simulation_dir/tests/wasm_test.mjs"
python3 "$simulation_dir/tests/rgba_to_png.py"
