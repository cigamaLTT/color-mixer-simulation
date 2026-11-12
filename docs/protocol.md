# MQTT và API phiên bản 1

Ba kênh luôn theo thứ tự **green, skin, blue**: CHINJOO 066, 046, 035. Tỉ lệ là tỉ lệ **khối lượng**, không phải phần trăm RGB hay thể tích. Màu trên dashboard là hình minh họa, không dự đoán chính xác màu sơn sau pha.

## MQTT

MQTT 3.1.1. Broker nhúng nghe TCP `127.0.0.1:1883` và MQTT-over-WebSocket `ws://127.0.0.1:9001` (subprotocol `mqtt`). WebSocket ứng dụng ở `/ws` cổng 8080 là một giao thức khác.

| Topic | Chiều | Nội dung |
| --- | --- | --- |
| `mixer/{device_id}/telemetry` | ESP32 → backend | Toàn bộ trạng thái đo và đầu ra điều khiển |
| `mixer/{device_id}/command` | backend → ESP32 | Lệnh JSON, QoS 1, **không retained** |
| `mixer/{device_id}/event` | ESP32 → backend | Xác nhận lệnh, chuyển trạng thái, hoàn tất hoặc lỗi |
| `mixer/{device_id}/status` | ESP32 → backend | Trạng thái kết nối / Last Will |

Lệnh `start`:

```json
{"id":"batch-001","session_id":"boot-...","expires_uptime_ms":6000,"type":"start","total_g":100,"ratios":[50,30,20]}
```

`id` dùng để ghép xác nhận với lệnh. `total_g` phải từ 1 đến 875 g; ba tỉ lệ hữu hạn, không âm và tổng bằng 100. Một kênh có thể bằng 0. Giới hạn 875 g giả định cốc 120 g trên loadcell 1 kg, còn 5 g dự phòng; firmware còn kiểm tra khối lượng tổng đo thực tế trước và trong mẻ. Các lệnh khác là `stop`, `reset`, `tare`. `start/reset/tare` cần `session_id` hiện tại cùng `expires_uptime_ms` chưa hết hạn và không quá 60 giây trong tương lai, tính theo uptime của ESP32. Backend thêm session và hạn 5 giây từ telemetry gần nhất. `stop` được firmware chấp nhận không phụ thuộc session/hạn. Không ghi retained vào topic command.

Telemetry mẫu đầy đủ nằm ở [`tests/fixtures/telemetry.json`](../tests/fixtures/telemetry.json). Đây là fixture kiểm thử, không được backend dùng làm dữ liệu thiết bị.

| Trường | Ý nghĩa |
| --- | --- |
| `schema_version`, `device_id`, `session_id` | Phiên bản 1, định danh thiết bị, phiên khởi động |
| `seq`, `uptime_ms` | Số thứ tự và thời gian đơn điệu trên thiết bị |
| `state`, `channel` | Trạng thái và kênh đang định lượng; `channel=null` khi không có kênh |
| `weight_g`, `gross_weight_g` | Khối lượng đã tare và khối lượng tổng trên cân |
| `target_g`, `delivered_g[3]` | Mục tiêu mẻ và lượng cấp từng kênh |
| `levels_pct[3]` | Mức ba bồn từ cảm biến mô phỏng / hiệu chuẩn |
| `pumps[3]`, `stirrers[4]` | Bơm chuẩn hóa 0..1 (hiện bật/tắt) và khuấy ba bồn + cốc nhận |
| `scale_valid`, `fault` | Tính hợp lệ phép đo cân và lỗi hiện tại (`null` nếu không lỗi) |

Các trạng thái: `idle`, `stirring`, `taring`, `dosing`, `settling`, `mixing`, `complete`, `stopped`, `fault`. Xác nhận từ thiết bị:

```json
{"id":"batch-001","type":"command_ack","accepted":true,"state":"stirring"}
```

Xác nhận `accepted=false` có `reason`. HTTP `202` hoặc WebSocket `command_ack` của backend chỉ xác nhận **đã phát MQTT**; sự kiện `event` trên dashboard mới chứa kết quả chấp nhận từ firmware.

## HTTP và WebSocket ứng dụng

`GET /healthz` kiểm tra tiến trình HTTP và trả `broker_connected`. `GET /api/state` trả snapshot:

```json
{"devices":[{"device_id":"mixer-01","telemetry":null,"online":false,"stale":true,"last_seen":null}],"broker":{"mode":"embedded","connected":true},"stale_after_ms":5000,"offline_after_ms":15000}
```

Trường địa chỉ broker và các thiết bị có thể khác. Không có phép đo thì `telemetry=null`; backend không tự dựng một mẻ giả.

`POST /api/command`, `Content-Type: application/json`:

```json
{"device_id":"mixer-01","command":{"id":"batch-001","type":"start","total_g":100,"ratios":[50,30,20]}}
```

Thành công trả HTTP `202` cùng `{id,device_id,status:"published",message}`. Lỗi trả `{error:{code,message},id?}`.

Kết nối `ws://127.0.0.1:8080/ws`. Bản tin đầu là `{type:"state",data:Snapshot}`. Gửi cùng nội dung HTTP nhưng thêm `type:"command"`. Server phát `{type,data}` với `type` là `telemetry`, `status`, `event`, `command_ack`, `error`. Lỗi lệnh có `data:{code,message,id?}`. UI dùng `last_seen`, `stale` và trạng thái kết nối để báo dữ liệu cũ; một WebSocket đang mở không chứng minh ESP32 đang hoạt động.

## Cấu hình backend

| Biến môi trường | Mặc định | Mục đích |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | Dashboard, HTTP, app WebSocket |
| `MQTT_ADDR` | `127.0.0.1:1883` | Broker nhúng TCP |
| `MQTT_WS_ADDR` | `127.0.0.1:9001` | Broker nhúng MQTT-over-WebSocket |
| `DEVICE_ID` | `mixer-01` | Thiết bị mặc định |
| `STALE_AFTER` / `OFFLINE_AFTER` | `5s` / `15s` | Ngưỡng cũ / mất thiết bị |
| `MQTT_URL` | rỗng | Dùng broker ngoài, ví dụ `tcp://broker.example:1883` |
| `MQTT_USERNAME` / `MQTT_PASSWORD` | rỗng | Thông tin xác thực broker ngoài nếu cần |

Broker nhúng mặc định phục vụ phát triển trên loopback. Khi dùng broker ngoài, thiết bị và backend phải cùng broker và cùng topic/device_id. Không đưa mật khẩu vào Git hoặc ảnh chụp.
