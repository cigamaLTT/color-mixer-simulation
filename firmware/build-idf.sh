#!/usr/bin/env bash
set -euo pipefail
firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [[ -f "$firmware_dir/tools/export-esp.sh" ]]; then
  source "$firmware_dir/tools/export-esp.sh"
elif [[ -f "$HOME/export-esp.sh" ]]; then
  source "$HOME/export-esp.sh"
fi
export PATH="$firmware_dir/tools/idf/bin:$PATH"
cd "$firmware_dir/esp32-idf"
export WIFI_SSID="${WIFI_SSID:-Wokwi-GUEST}"
export WIFI_PASSWORD="${WIFI_PASSWORD:-}"
export MQTT_HOST="${MQTT_HOST:-host.wokwi.internal}"
export MQTT_PORT="${MQTT_PORT:-1883}"
export DEVICE_ID="${DEVICE_ID:-mixer-01}"
export LEVEL_ADC_FULL="${LEVEL_ADC_FULL:-2703}"
export CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-2}"
cargo +esp build --release
cp target/xtensa-esp32-espidf/release/esp32-mixer-idf "$firmware_dir/esp32-mixer-idf.elf"
printf '%s\n' "ESP32 ESP-IDF firmware: $firmware_dir/esp32-mixer-idf.elf"
