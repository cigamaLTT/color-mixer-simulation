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

## ESP32 trong Wokwi — chưa đạt vòng truyền đầy đủ

Firmware `no_std` đã build thành ELF Xtensa. Khi chạy trong Wokwi, UART đã ghi nhận cân HX711 và mức ba bồn từ plant C/WASM. Wi-Fi chưa kết nối thành công; quá trình chạy gặp lỗi `IllegalInstruction`. Chưa có mẻ ESP32 hoàn tất qua MQTT và dashboard.

`firmware/esp32-idf/` chứa phương án ESP-IDF đang viết dở. Phương án này chưa được build hoặc chạy thử; `wokwi.toml` vẫn dùng firmware `no_std`.

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
