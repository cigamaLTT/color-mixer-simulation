#!/usr/bin/env bash
set -euo pipefail

firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tools_dir="$firmware_dir/tools"
export PATH="$tools_dir/idf-prefix/bin:$PATH"
export LD_LIBRARY_PATH="$tools_dir/idf-prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

# Prefer the project-local Python 3.12 installation on newer host systems.
for python_bin in "$tools_dir"/python/cpython-3.12.*/bin; do
  if [[ -x "$python_bin/python3" ]]; then
    export PATH="$python_bin:$PATH"
    break
  fi
done

toolchain_root=$(rustc +esp --print sysroot)
for clang_lib in "$toolchain_root"/xtensa-esp32-elf-clang/*/esp-clang/lib; do
  if [[ -d "$clang_lib" ]]; then
    export LIBCLANG_PATH="$clang_lib"
    break
  fi
done

export CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-2}"
cd "$firmware_dir/esp32-idf"
cargo +esp build --locked --release --example wifi-smoke
cp target/xtensa-esp32-espidf/release/examples/wifi-smoke "$firmware_dir/esp32-wifi-smoke.elf"
printf '%s\n' "ESP32 WiFi smoke firmware: $firmware_dir/esp32-wifi-smoke.elf"
