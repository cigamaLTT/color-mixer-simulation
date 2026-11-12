#!/usr/bin/env bash
set -euo pipefail
firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tools_dir="$firmware_dir/tools"
export PATH="$tools_dir/idf-prefix/bin:$PATH"
export LD_LIBRARY_PATH="$tools_dir/idf-prefix/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

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

cd "$firmware_dir/esp32-idf"
export WIFI_SSID="${WIFI_SSID:-Wokwi-GUEST}"
export WIFI_PASSWORD="${WIFI_PASSWORD:-}"
export MQTT_HOST="${MQTT_HOST:-host.wokwi.internal}"
export MQTT_PORT="${MQTT_PORT:-1883}"
export DEVICE_ID="${DEVICE_ID:-mixer-01}"
export LEVEL_ADC_FULL="${LEVEL_ADC_FULL:-2703}"
export CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-2}"
cargo +esp build --locked --release --bin esp32-mixer-idf
cp target/xtensa-esp32-espidf/release/esp32-mixer-idf "$firmware_dir/esp32-mixer-idf.elf"
printf '%s\n' "ESP32 ESP-IDF firmware: $firmware_dir/esp32-mixer-idf.elf"
