#!/usr/bin/env bash
set -euo pipefail
firmware_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [[ -f "$firmware_dir/tools/export-esp.sh" ]]; then
  source "$firmware_dir/tools/export-esp.sh"
elif [[ -f "$HOME/export-esp.sh" ]]; then
  source "$HOME/export-esp.sh"
fi
cd "$firmware_dir/esp32"
export WIFI_SSID="${WIFI_SSID:-Wokwi-GUEST}"
export WIFI_PASSWORD="${WIFI_PASSWORD:-}"
export MQTT_HOST="${MQTT_HOST:-host.wokwi.internal}"
export MQTT_PORT="${MQTT_PORT:-1883}"
export DEVICE_ID="${DEVICE_ID:-mixer-01}"
export LEVEL_ADC_FULL="${LEVEL_ADC_FULL:-2703}"
cargo +esp build --locked --release -p esp32-mixer
cp "$firmware_dir/target/xtensa-esp32-none-elf/release/esp32-mixer" "$firmware_dir/esp32-mixer.elf"
# Wokwi loads the ELF directly, so .bin conversion is optional.
if command -v espflash >/dev/null; then
  espflash save-image --chip esp32 "$firmware_dir/esp32-mixer.elf" "$firmware_dir/esp32-mixer.bin"
fi
printf '%s\n' "ESP32 firmware: $firmware_dir/esp32-mixer.elf"
