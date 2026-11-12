# BOM, pinout và giả định phần cứng

Cấu hình nguyên mẫu dùng để đối chiếu mô phỏng với mạch thật. Chân trên module cần kiểm tra theo sơ đồ của module trước khi nối.

## Linh kiện

| Hạng mục | Số lượng / cấu hình | Vai trò / điểm cần kiểm tra |
| --- | --- | --- |
| ESP32 CH340-C 30 chân | 1 | Mô phỏng bằng ESP32 DevKit v1; đối chiếu nhãn GPIO thật |
| CHINJOO green 066, skin 046, blue 035 | 3 màu | Ba bồn nguồn, cấp tuần tự theo khối lượng |
| Bơm DC 3.7–7.4 V | 3 | Đường 6 V; đo dòng khởi động/kẹt và lưu lượng với sơn |
| Motor khuấy DC 6 V | 3 | Khuấy ba bồn nguồn |
| DRV8833 | 3 module hai cầu H | Đủ 6 kênh cho 3 bơm + 3 motor nguồn; không đủ thêm motor cốc |
| Motor khuấy cốc nhận + driver bổ sung | 1 motor | Kênh thứ 7 cần driver ngoài 3 module DRV8833 |
| Phao xe máy 3 dây Wave RS/Funeo | 3 trong mô hình | Mỗi bồn cần một cảm biến; chưa xác định chân theo màu dây |
| Loadcell + HX711 | Loadcell giả định 1 kg | Xác minh thông số tem và hệ số counts/g, zero/tare |
| Nguồn 12 V 5 A, XL4016 | Đường nguồn motor | Hạ áp khoảng 6 V; cần đo tải thực |
| LM2596S | Đường nguồn logic | Cấp 5 V vào chân nguồn phù hợp của board |
| Ống silicone 3×5 / 4×6 | Đường sơn | Kiểm tra đường kính với đầu bơm, tương thích sơn và chống rò |

Cấp nguồn motor qua driver; GPIO chỉ điều khiển logic. Nối mass tham chiếu giữa logic, driver và cảm biến theo thiết kế nguồn. Không cấp 5 V trực tiếp vào chân ADC ESP32. Với phao điện trở/chiết áp, đo chức năng từng dây và thiết kế chia áp, lọc, giới hạn điện áp rồi hiệu chuẩn mức; không suy luận pinout từ tên xe hoặc màu dây.

## Ánh xạ tín hiệu mô phỏng

| Chức năng | ESP32 GPIO |
| --- | --- |
| Bơm green / skin / blue | 18 / 19 / 21 |
| Khuấy bồn green / skin / blue | 22 / 23 / 25 |
| Khuấy cốc nhận | 26 |
| Mức bồn green / skin / blue (ADC) | 32 / 33 / 34 |
| HX711 DOUT / SCK | 35 / 27 |

GPIO 34 và 35 là tín hiệu vào. Pinout trên là ánh xạ của firmware/diagram; chưa phải bản mạch đã được xác nhận về điện. Mô hình chip theo dõi tín hiệu điều khiển và giả lập cả phần tải/chất lỏng; việc một motor trong hình quay không chứng minh cách đấu DRV8833 ngoài thực tế đã đúng.

## Thông số plant ban đầu

Ba bồn 1000 ml, mật độ 1 g/ml, mỗi bơm tối đa 20 ml/s, cốc 120 g. HX711 mô phỏng 1000 counts/g khối lượng tổng, mẫu mới mỗi 100 ms. Loadcell tối đa 1000 g gồm cốc; API giới hạn mẻ 875 g và controller còn kiểm tra số đo thực.

Các thông số này làm mô hình chạy lặp lại để kiểm tra phần mềm. Sơn có độ nhớt, mật độ, bám ống và độ trễ khác nhau; cần đo rồi thay tham số khi lắp thật. Không dùng màu hiển thị để đánh giá độ chính xác màu vật liệu.
