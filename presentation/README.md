# COLMIX

`colmix.pdf` là bộ slide 9 trang, `colmix.tex` là mã nguồn LaTeX Beamer

## Dựng PDF

Cần Tectonic và phông Noto Sans hỗ trợ tiếng Việt

```bash
./presentation/build.sh
```

Có thể đặt `TECTONIC` trỏ đến chương trình Tectonic, lần dựng đầu tải các gói TeX cần thiết

## Chụp lại hình minh họa

Cần backend/dashboard tại `http://127.0.0.1:8088`, MQTT tại `127.0.0.1:1883`, binary `firmware/target/release/virtual-device`, Python Playwright và Chromium

```bash
python3 presentation/capture_screenshots.py
./presentation/build.sh
```

Script khởi chạy thiết bị native riêng `presentation-demo`, đặt mẻ 100 g theo tỉ lệ 50/30/20, chụp thiết lập, định lượng, khuấy và hoàn tất, rồi kết thúc tiến trình native do script tạo

`assets/` chứa ảnh chụp trực tiếp các vùng giao diện, `evidence/capture.json` ghi trạng thái thiết bị và các sự kiện của phiên chụp, `evidence/native.log` ghi dữ liệu từ bộ điều khiển

Ảnh minh họa dùng controller Rust và plant phần mềm chạy trên máy tính, truyền qua MQTT đến backend Go rồi cập nhật dashboard qua WebSocket, slide kiến trúc thể hiện thiết kế đề xuất với ESP32 và Wi-Fi

Nội dung phần cứng được đối chiếu với `../docs/hardware.md`, luồng điều khiển được đối chiếu với `../docs/protocol.md`
