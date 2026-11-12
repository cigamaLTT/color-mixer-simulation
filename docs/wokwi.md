# Mô phỏng Wokwi

ESP32 `no_std` đã đọc HX711 và mức bồn từ custom chip trong Wokwi. Wi-Fi chưa kết nối thành công và runtime gặp `IllegalInstruction`, nên chưa có vòng điều khiển qua MQTT/dashboard. Phương án `firmware/esp32-idf/` chưa hoàn thiện hoặc build; cấu hình Wokwi vẫn nạp firmware `no_std`.

## Build và khởi động

Cần Go 1.24 trở lên, Rust stable, toolchain Rust Xtensa `esp` và Clang hỗ trợ `wasm32`:

```bash
./firmware/setup-toolchain.sh
make build
HTTP_ADDR=127.0.0.1:8088 make run
```

Script setup tải và cài toolchain; xem [firmware/README.md](../firmware/README.md) để biết phiên bản. `make build` tạo `firmware/esp32-mixer.elf` và `simulation/build/mixer.chip.wasm` theo đường dẫn trong `wokwi.toml`.

Với Wokwi for VS Code đã kích hoạt, mở thư mục gốc và chạy **Wokwi: Start Simulator**. CLI nhận token qua biến môi trường:

```bash
read -rsp 'Wokwi CLI token: ' WOKWI_CLI_TOKEN
export WOKWI_CLI_TOKEN
make wokwi
```

`scripts/wokwi.sh` tìm `wokwi-cli` trên `PATH`, trong `.tools/`, hoặc theo `WOKWI_CLI`. Token và thông tin kích hoạt không thuộc mã nguồn. Xem [hướng dẫn VS Code](https://docs.wokwi.com/vscode/getting-started) và [Wokwi CLI](https://docs.wokwi.com/wokwi-ci/cli-usage).

## Kết nối broker

Firmware mặc định dùng Wi-Fi `Wokwi-GUEST` không mật khẩu và broker `host.wokwi.internal:1883`. Broker cục bộ cần gateway cho phép mô phỏng truy cập máy host. `127.0.0.1` trong firmware trỏ vào thiết bị mô phỏng. Xem [Wi-Fi và gateway](https://docs.wokwi.com/guides/esp32-wifi) và [cấu hình VS Code](https://docs.wokwi.com/vscode/project-config).

Backend mặc định phục vụ MQTT TCP ở `127.0.0.1:1883`. Nếu dùng broker ngoài, cấu hình `MQTT_HOST`/`MQTT_PORT` lúc build firmware và `MQTT_URL` lúc chạy backend tới cùng broker:

```bash
MQTT_HOST=broker.example ./firmware/build-esp32.sh
MQTT_URL=tcp://broker.example:1883 HTTP_ADDR=127.0.0.1:8088 make run
```

`broker.example` cần thay bằng hostname thực. Firmware dùng MQTT TCP không TLS và không xác thực broker; `MQTT_USERNAME`/`MQTT_PASSWORD` của backend không cấu hình thông tin đăng nhập cho ESP32.

Sau khi khắc phục lỗi runtime, UART cần báo Wi-Fi/MQTT kết nối và dashboard phải nhận telemetry của phiên `esp32-…`. Kết nối WebSocket giữa trình duyệt và backend chỉ phản ánh đường truyền giao diện.

## Kiểm tra vòng phản hồi

Khi ESP32 trực tuyến và `idle`, cốc rỗng, mức bồn đủ và cân hợp lệ:

```bash
mkdir -p .logs
set -o pipefail
make verify-wokwi VERIFY_WOKWI_ARGS=--check-stop | tee .logs/wokwi-acceptance.ndjson
```

`--check-stop` gửi mẻ thử, dừng bằng HTTP sau khi bơm bật, reset rồi chạy mẻ 100 g 50/30/20 qua app WebSocket. Trình kiểm tra yêu cầu phiên `esp32-…`, đối chiếu xác nhận firmware, lệnh MQTT không retained và telemetry hoàn tất trên HTTP cùng hai loại WebSocket. Sai lệch cho phép là 3 g tổng và 2 g mỗi kênh. Nếu gặp lỗi, trình cố gửi `stop` qua HTTP rồi thoát khác 0.

Đổi địa chỉ hoặc thời gian chờ:

```bash
python3 scripts/verify-wokwi.py --base-url http://127.0.0.1:8088 \
  --mqtt-host 127.0.0.1 --mqtt-ws-port 9001 --device-id mixer-01 \
  --wait-online 180 --timeout 600 --check-stop
```

Để ghi traffic mà không gửi lệnh:

```bash
mkdir -p .logs
python3 scripts/observe-device.py --duration 120 > .logs/wokwi-mqtt.ndjson
```

MQTT WebSocket dùng `ws://127.0.0.1:9001/mqtt`, subprotocol `mqtt`. WebSocket ứng dụng dùng `ws://127.0.0.1:8088/ws` với JSON riêng. Các control tắc bơm, hết sơn và mất cân được mô tả ở [simulation/README.md](../simulation/README.md). Tiêu chí và kết quả kiểm thử nằm ở [validation.md](validation.md).
