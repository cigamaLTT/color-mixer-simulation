Run the minimal Rust `std` / ESP-IDF WiFi check on a classic ESP32:

```sh
bash firmware/setup-idf-tools.sh
bash firmware/build-idf-wifi.sh
```

The build uses the ESP Rust toolchain from `firmware/setup-toolchain.sh`,
ESP-IDF 5.3.3, and project-local Python 3.12. The first build downloads the
ESP-IDF source and matching native tools. Later builds reuse them.

Open this `simulation/wifi-smoke` directory in VS Code with the Wokwi extension
and run **Wokwi: Start Simulator**. The example connects to the open
`Wokwi-GUEST` network on channel 6 and waits for DHCP. A successful run prints
`WIFI_CONNECTED ip=... gateway=... subnet=...` with the address returned by
ESP-IDF. The driver remains alive afterward.

The source follows the [official blocking WiFi example](https://github.com/esp-rs/esp-idf-svc/blob/v0.51.0/examples/wifi.rs).

Validated on 2026-11-12 with the classic ESP32 and Wokwi VS Code extension 4.1.0:

```text
I (3090) wifi:connected with Wokwi-GUEST, aid = 1, channel 6
I (4129) esp_netif_handlers: sta ip: 10.13.37.2, mask: 255.255.255.0, gw: 10.13.37.1
WIFI_CONNECTED ip=10.13.37.2
```

This verifies WiFi association and DHCP for the minimal example. The test
does not exercise MQTT or the mixer application. On the validation host,
Wokwi ran at approximately 3% speed, so four simulated seconds took several
minutes of wall time.
