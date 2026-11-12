# Mô hình máy pha 2D trong Wokwi

Custom chip `mixer.chip.c` là plant C/WASM, kết nối với ESP32 bằng GPIO. Chip nhận ba tín hiệu bật/tắt bơm, bốn tín hiệu khuấy; nó trả ba điện áp mức bồn và bitstream HX711. Firmware Rust điều khiển dựa trên các phép đo này. Không có kết nối tắt từ plant tới MQTT hoặc dashboard.

`diagram.json` gốc đặt ESP32 và plant 760 × 520 pixel cạnh nhau. Các bồn, ống, bơm, cốc và cánh khuấy trong framebuffer thể hiện trạng thái GPIO và lượng sơn của mô hình. Hiệu ứng dòng chảy chỉ xuất hiện khi bơm thực sự chuyển sơn; bơm bị tắc có thể báo đang bật nhưng không có dòng chảy.

## Đơn vị và phản hồi

Ba nguồn theo thứ tự **green 066 / skin 046 / blue 035**, mỗi bồn chứa 1000 ml. Mô hình dùng mật độ 1 g/ml và lưu lượng mặc định 20 ml/s khi bơm bật. Mỗi bước tích phân giữ phần lẻ thể tích để tránh mất khối lượng khi bước thời gian nhỏ. Lượng rời nguồn bằng lượng vào cốc; không có sơn tự tăng theo thời gian khi GPIO bơm tắt.

Mức nguồn xuất 0–3,3 V tương ứng 0–100%; firmware dùng ADC1 ở GPIO32/33/34. Wokwi dùng tham chiếu ADC ảo 5 V, nên điểm 3,3 V tương ứng khoảng 2703 counts. Firmware mặc định dùng đúng điểm này qua `LEVEL_ADC_FULL=2703`.

HX711 xuất khối lượng **tổng** gồm cốc 120 g và sơn, 1000 counts/g, chu kỳ chuyển đổi 100 ms. ESP32 đọc 24 bit có dấu sau sườn lên SCK và phát xung thứ 25 cho gain 128. DOUT giữ mẫu đã chốt cho tới khi đọc xong; mức SCK cao ít nhất 60 µs đưa HX711 vào power-down. Tare và khối lượng mẻ thuộc controller Rust; plant luôn trả tổng khối lượng vật lý.

## Điều khiển trong Wokwi

Chọn custom chip để mở các controls. Với `refillSources` và `drainReceiver`, đổi số sang giá trị khác để thực hiện một lần. Các controls khác giữ hiệu lực theo giá trị hiện tại.

| Control | Giá trị | Tác dụng |
| --- | --- | --- |
| `emptyGreen`, `emptySkin`, `emptyBlue` | 0 hoặc 1 | Ép bồn tương ứng về rỗng; trả về 0 không tự nạp sơn |
| `blockGreen`, `blockSkin`, `blockBlue` | 0 hoặc 1 | Chặn lưu lượng bơm nhưng giữ trạng thái GPIO bật/tắt |
| `scaleNoiseG` | 0–5 g | Nhiễu có biên độ ± giá trị đặt, thay đổi theo mẫu cân |
| `disconnectScale` | 0 hoặc 1 | Giữ HX711 chưa sẵn sàng, làm firmware phát hiện mất cân |
| `refillSources` | 0–100, đổi số | Nạp đầy cả ba nguồn; tắt các `empty…` trước khi nạp |
| `drainReceiver` | 0–100, đổi số | Đổ sơn khỏi cốc, giữ khối lượng cốc |
| `flowMlPerSecond` | 5–80 ml/s | Thay lưu lượng mô phỏng; controller cần hiệu chỉnh khi dùng lưu lượng khác |
| `cupMassG` | 0–300 g | Khối lượng cốc; vẫn tính vào giới hạn loadcell 1 kg |

Quy trình lỗi thực tế trong Wokwi:

1. Chạy một mẻ 100 g 50/30/20 và chờ bơm green bật.
2. Đặt `blockGreen=1`; cân phải ngừng tăng, firmware chuyển `fault` do không có dòng chảy và tắt toàn bộ bơm/khuấy.
3. Bỏ chặn, nhấn **Đặt lại**; nếu cần đổ cốc bằng `drainReceiver`, chờ mẫu cân ổn định rồi tare.
4. Lặp lại với `disconnectScale=1` khi đang định lượng; cân phải báo không hợp lệ và mọi đầu ra tắt.
5. Đặt một `empty…=1`; mẻ cần nguồn đó phải bị từ chối hoặc vào lỗi mức thấp. Bỏ ép rỗng và đổi `refillSources` để phục hồi.

Lỗi bị giữ cho tới `reset`; không tiếp tục mẻ cũ sau khi cảm biến phục hồi hoặc MQTT kết nối lại. Ghi UART, telemetry và sự kiện của cùng `session_id` khi kiểm chứng.

## Build và kiểm thử độc lập

```bash
make build-simulation
make test-simulation
```

Build dùng Clang `--target=wasm32-unknown-unknown`, `-Wall -Wextra -Werror`, không có libc. Kết quả nằm trong `simulation/build/mixer.chip.wasm` và JSON đi kèm. Root `wokwi.toml` nạp chip này cùng ELF Rust ESP32.

Kiểm thử C kiểm tra bảo toàn lượng sơn, mức bồn, tắc/rỗng/nạp/đổ, khối lượng tổng và giao thức HX711. Kiểm thử Node nạp **WASM đã biên dịch**, nối pin/timer/framebuffer bằng một host Chips API nhỏ và kiểm tra các controls cùng phản hồi ADC/HX711. `simulation/build/preview.png` được dựng từ framebuffer của bài kiểm thử này. Nó là preview plant trên host; bằng chứng firmware ESP32 thực thi trong Wokwi phải có UART và MQTT tương ứng theo [docs/validation.md](../docs/validation.md).
