# Firmware Rust

| Thư mục | Vai trò và trạng thái |
| --- | --- |
| `mixer-core/` | Controller `no_std`, giao thức JSON và MQTT dùng chung |
| `virtual-device/` | Thiết bị native với plant phần mềm; đã hoàn tất mẻ 100 g trong kiểm thử tích hợp |
| `esp32/` | Firmware ESP32 classic dùng `esp-hal` và `esp-wifi`; build được, đọc cảm biến trong Wokwi, nhưng Wi-Fi chưa kết nối và runtime gặp `IllegalInstruction` |
| `esp32-idf/` | Phương án ESP-IDF dùng Rust `std`; đang viết dở, chưa build hoặc kiểm chứng |

`wokwi.toml` ở thư mục gốc nạp ELF `no_std`. Vòng ESP32 → MQTT → dashboard chưa được xác nhận; xem [kết quả kiểm thử](../docs/validation.md).

## Build ESP32 `no_std`

```sh
./firmware/setup-toolchain.sh
./firmware/build-esp32.sh
```

Script setup dùng espup 0.19.0 để cài toolchain Espressif Rust 1.86.0 cho ESP32. Build dùng `Cargo.lock` với `--locked`, xuất `firmware/esp32-mixer.elf`; nếu có `espflash`, script tạo thêm `.bin`. Wokwi nạp ELF trực tiếp. Kiểm thử host dùng Rust stable.

Phương án ESP-IDF có workspace riêng và script `./firmware/build-idf.sh`, cần thêm toolchain ESP-IDF và linker `ldproxy`. Cấu hình ghim ESP-IDF v5.3.3; script dự kiến xuất `firmware/esp32-mixer-idf.elf`. Quy trình này chưa được build thử và chưa được chọn trong `wokwi.toml`.

## Cấu hình lúc build

| Biến | Mặc định | Ý nghĩa |
| --- | --- | --- |
| `WIFI_SSID` | `Wokwi-GUEST` | Tên mạng Wi-Fi |
| `WIFI_PASSWORD` | rỗng | Mật khẩu Wi-Fi |
| `MQTT_HOST` | `host.wokwi.internal` | Broker qua gateway Wokwi; hỗ trợ IPv4 |
| `MQTT_PORT` | `1883` | Cổng MQTT TCP |
| `DEVICE_ID` | `mixer-01` | Chữ, số, `_` hoặc `-`, tối đa 40 byte |
| `LEVEL_ADC_FULL` | `2703` | Số đọc ADC khi bồn đầy |
| `SCALE_COUNTS_PER_G` | `1000` | Counts HX711 trên một gram |
| `SCALE_OFFSET_COUNTS` | `0` | Offset trước khi đổi sang gram tổng |

Ví dụ thay địa chỉ broker:

```sh
MQTT_HOST=192.168.1.10 ./firmware/build-esp32.sh
```

Firmware dùng MQTT TCP không TLS và không gửi thông tin xác thực broker. Các giá trị Wi-Fi được nhúng trong binary; sản phẩm build không được đưa vào Git. ADC và cân trên mạch thật cần hiệu chuẩn. Trong plant Wokwi, 3,3 V tương ứng khoảng 2703 counts với tham chiếu ADC ảo 5 V.

## GPIO và điều khiển

| Chức năng | ESP32 GPIO |
| --- | --- |
| Bơm green / skin / blue | 18 / 19 / 21 |
| Khuấy bồn green / skin / blue | 22 / 23 / 25 |
| Khuấy cốc nhận | 26 |
| Mức bồn green / skin / blue | 32 / 33 / 34 (ADC1) |
| HX711 DOUT / SCK | 35 / 27 |

Đầu ra active high, khởi tạo ở mức thấp. Motor cần driver ngoài, bảo vệ tải cảm và điện trở kéo xuống. GPIO35 chỉ nhận tín hiệu và không có điện trở kéo nội. HX711 được đọc 24 bit có dấu, sau đó xung thứ 25 chọn gain 128. Các xung SCK cao 2 µs nằm trong critical section để tránh power-down do ngắt kéo dài.

Loadcell giới hạn 1000 g tổng gồm cốc và sơn. Mẻ cho phép 1–875 g, đồng thời khối lượng tổng hiện tại cộng lượng yêu cầu không được vượt 995 g. Controller khuấy nguồn, tare ổn định, cấp tuần tự theo cân, chờ ổn định từng kênh rồi khuấy cốc. Cấp tinh dùng xung 40 ms, nghỉ 310 ms ở lưu lượng mô phỏng 20 g/s. Kênh có tỉ lệ 0 được bỏ qua; thời gian và hệ số cần hiệu chuẩn cho bơm thật.

Các lỗi tắt mọi đầu ra và giữ đến `reset`: cân mất dữ liệu/quá tải, mức nguồn thấp hoặc không hợp lệ, khối lượng giảm, không có dòng chảy, quá thời gian và vòng điều khiển chậm quá 100 ms. Firmware `no_std` có watchdog TIMG1 250 ms và xóa đầu ra GPIO trong panic handler. Hành vi trên phần cứng thật chưa được kiểm chứng.

MQTT dùng clean session, retained online status và offline Last Will. Lệnh QoS 1 được xác nhận bằng PUBACK và sự kiện `command_ack`; telemetry/sự kiện dùng QoS 0. `start`, `tare`, `reset` cần session và hạn uptime hợp lệ; lệnh retained bị từ chối. Stop tắt đầu ra cả khi session/hạn cũ hoặc ID lặp. Runtime nhớ 32 ID để tránh thực thi lại và không tiếp tục mẻ sau reconnect. Xem [giao thức](../docs/protocol.md).

## Thiết bị native

```sh
cargo test --locked --manifest-path firmware/Cargo.toml -p mixer-core
cargo run --locked --release --manifest-path firmware/Cargo.toml -p virtual-device -- \
  --broker 127.0.0.1:1883 --device-id mixer-01 --speed 10 --verbose
```

Plant native dùng cốc 120 g, ba bồn 1000 g, bơm 20 g/s và mẫu cân giữ 100 ms. Mô hình tích phân mỗi 10 ms kể cả khi tăng tốc. Tùy chọn `--fault no_flow`, `--fault scale_failure`, `--fault empty_green` gây lỗi; `--duration SECONDS` giới hạn thời gian thực. Sự kiện xuất ra stdout, thông tin kết nối ra stderr, `--verbose` in thêm telemetry.

Tham khảo: [toolchain Rust ESP](https://docs.espressif.com/projects/rust/book/getting-started/toolchain.html), [esp-hal 0.23.1](https://docs.rs/esp-hal/0.23.1/esp_hal/), [cấu hình Wokwi](https://docs.wokwi.com/vscode/project-config).
