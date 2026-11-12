# Kết quả kiểm thử

## Host — 2026-11-12

```bash
make test
```

| Thành phần | Kết quả |
| --- | --- |
| Rust `mixer-core` | 9 unit tests đạt: định lượng, giới hạn cân, stop, lỗi cân, mất MQTT và lệnh cũ/retained/hết hạn |
| Controller bổ sung | 4 bài đạt: mẫu cân 10 SPS, độ lệch pha lấy mẫu và giới hạn khối lượng tổng |
| Go backend | `go test -race ./...` đạt: telemetry, lệnh, session, dữ liệu cũ, Last Will, HTTP và WebSocket |
| Plant C | Đạt: bảo toàn lượng sơn, ADC, rỗng/tắc/nạp/đổ, quá tải và HX711 |
| WASM | Đạt: GPIO, ba mức analog, HX711 24+1 xung, control gây lỗi và framebuffer bằng host Chips API của bài kiểm thử |
| Tích hợp socket | 4 bài đạt: dashboard chờ dữ liệu; MQTT TCP ↔ MQTT WebSocket và HTTP/app WebSocket ↔ MQTT; mẻ native 100 g và stop; lỗi no-flow tắt mọi đầu ra |

Bộ tích hợp khởi động backend/broker riêng trên các cổng ngẫu nhiên và dọn tiến trình sau kiểm thử. Thiết bị native có `session_id=native-…`. Bài kiểm thử này chạy controller và MQTT runtime trên máy, không thực thi firmware Xtensa.

Dashboard đã được kiểm tra bằng Chromium ở 1440 × 1100 và 390 × 844: app WebSocket kết nối, không có lỗi JavaScript hoặc tràn ngang. Khi thiếu telemetry, giao diện báo chờ dữ liệu và vô hiệu hóa bắt đầu.

## ESP32 ESP-IDF trong Wokwi — đạt một mẻ thực tế mô phỏng (2026-11-13)

Đã build `firmware/esp32-idf/` bằng ESP-IDF 5.3.3 và chạy một mẻ từ firmware Xtensa trong Wokwi. Runner tạo backend/broker riêng tại HTTP 8089, MQTT 1884 và MQTT-over-WebSocket 9002; firmware dùng `host.wokwi.internal:1884` với device `mixer-idf-trial`. UART xác nhận DHCP `10.13.37.2` và MQTT connected trước khi nhận lệnh.

Lệnh đã chạy:

```bash
MQTT_PORT=1884 DEVICE_ID=mixer-idf-trial bash firmware/build-idf.sh
make verify-idf-wokwi
```

Mẻ 100 g, tỉ lệ 50/30/20, đi qua `stirring → taring → dosing → settling → mixing → complete`. Completion telemetry giống nhau trên HTTP snapshot, app WebSocket và MQTT-over-WebSocket; lệnh MQTT có QoS 1 và không retained. Các giá trị cuối là 100.74 g tổng, lần lượt 50.05 g / 30.40 g / 20.29 g; sai số tổng 0.74 g và sai số từng kênh 0.05 g / 0.40 g / 0.29 g. Mức ba bồn giảm từ 100% xuống 94.97% / 96.97% / 97.97%, và toàn bộ bơm/khuấy tắt ở `complete`.

Lượt này không chạy `--check-stop`. Mô phỏng vẫn là plant Wokwi, không xác nhận hiệu chuẩn, điện công suất hay độ tin cậy của máy thật. Trên máy kiểm chứng, 22.2 giây mô phỏng mất khoảng 11 phút thời gian thực.

## Tiêu chí nghiệm thu Wokwi

Sau khi sửa lỗi Wi-Fi/runtime, chạy backend và Wokwi, chờ ESP32 trực tuyến ở trạng thái `idle` rồi thực hiện:

```bash
mkdir -p .logs
set -o pipefail
make verify-wokwi VERIFY_WOKWI_ARGS=--check-stop | tee .logs/wokwi-acceptance.ndjson
```

Trình kiểm tra gửi lệnh điều khiển với các tiêu chí:

- Phiên `esp32-…` mới, cân hợp lệ và telemetry cập nhật.
- Stop sau khi bơm bật đưa mọi bơm/khuấy về tắt.
- Sau reset, mẻ 100 g với tỉ lệ 50/30/20 hoàn tất; cả ba bơm hoạt động và mức bồn giảm.
- Sai lệch tổng không quá 3 g, từng kênh không quá 2 g; mọi đầu ra tắt khi hoàn tất.
- Firmware xác nhận lệnh; lệnh xuất hiện trên MQTT WebSocket và không retained.
- Telemetry hoàn tất nhất quán trên app WebSocket, MQTT WebSocket và snapshot HTTP.

Kết quả của trình kiểm tra chưa được ghi nhận cho ESP32 trong Wokwi. Xem [wokwi.md](wokwi.md) để cấu hình và ghi traffic.
