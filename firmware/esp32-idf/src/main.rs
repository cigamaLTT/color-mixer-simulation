//! Classic ESP32 firmware using ESP-IDF's WiFi tasks and TCP/IP stack.
use esp_idf_svc::{
    eventloop::EspSystemEventLoop,
    hal::peripherals::Peripherals,
    nvs::EspDefaultNvsPartition,
    sys,
    wifi::{AuthMethod, ClientConfiguration, Configuration, EspWifi},
};
use mixer_core::{mqtt, Device, Sensors, SCALE_STALE_MS};
use std::{
    io::{self, Read, Write},
    net::{TcpStream, ToSocketAddrs},
    sync::mpsc::{self, Receiver, TryRecvError},
    thread,
    time::Duration,
};

const WIFI_SSID: &str = env!("WIFI_SSID");
const WIFI_PASSWORD: &str = env!("WIFI_PASSWORD");
const MQTT_HOST: &str = env!("MQTT_HOST");
const DEVICE_ID: &str = env!("DEVICE_ID");
const PUMPS: [i32; 3] = [18, 19, 21];
const STIRRERS: [i32; 4] = [22, 23, 25, 26];
const HX_DOUT: i32 = 35;
const HX_CLOCK: i32 = 27;

type ConnectResult = Result<TcpStream, String>;
fn millis() -> u64 {
    unsafe { sys::esp_timer_get_time() as u64 / 1000 }
}
fn outputs_off() {
    for pin in PUMPS.into_iter().chain(STIRRERS) {
        unsafe { sys::gpio_set_level(pin, 0); }
    }
}
fn apply_outputs(device: &Device) {
    for (i, pin) in PUMPS.into_iter().enumerate() {
        unsafe { sys::gpio_set_level(pin, u32::from(device.controller.outputs.pumps[i] > 0.0)); }
    }
    for (i, pin) in STIRRERS.into_iter().enumerate() {
        unsafe { sys::gpio_set_level(pin, u32::from(device.controller.outputs.stirrers[i])); }
    }
}
fn read_scale() -> Option<i32> {
    if unsafe { sys::gpio_get_level(HX_DOUT) } != 0 { return None; }
    // A WiFi interrupt must not stretch a high pulse beyond the HX711's
    // power-down threshold. This section lasts approximately 100us.
    Some(critical_section::with(|_| unsafe {
        let mut raw = 0u32;
        for _ in 0..24 {
            sys::gpio_set_level(HX_CLOCK, 1);
            sys::esp_rom_delay_us(2);
            raw = (raw << 1) | u32::from(sys::gpio_get_level(HX_DOUT) != 0);
            sys::gpio_set_level(HX_CLOCK, 0);
            sys::esp_rom_delay_us(2);
        }
        sys::gpio_set_level(HX_CLOCK, 1);
        sys::esp_rom_delay_us(2);
        sys::gpio_set_level(HX_CLOCK, 0);
        if raw & 0x80_0000 != 0 { (raw | 0xff00_0000) as i32 } else { raw as i32 }
    }))
}
fn connect_worker(port: u16) -> Receiver<ConnectResult> {
    let (sender, receiver) = mpsc::sync_channel(1);
    // DNS and TCP connection establishment can block. They run in an IDF task
    // while the main task continues reading sensors and applying safe outputs.
    let spawned = thread::Builder::new().name("mqtt-connect".into()).stack_size(12 * 1024).spawn(move || {
        let result = (|| -> io::Result<TcpStream> {
            let address = (MQTT_HOST, port).to_socket_addrs()?.find(|a| a.is_ipv4())
                .ok_or_else(|| io::Error::other("broker has no IPv4 address"))?;
            let stream = TcpStream::connect_timeout(&address, Duration::from_secs(3))?;
            stream.set_nonblocking(true)?;
            stream.set_nodelay(true)?;
            Ok(stream)
        })().map_err(|error| error.to_string());
        let _ = sender.send(result);
    });
    if let Err(error) = spawned { println!("MQTT_WORKER_ERROR {error}"); }
    receiver
}
fn main() {
    sys::link_patches();
    // Also cut outputs if a Rust panic occurs during an active batch.
    std::panic::set_hook(Box::new(|info| { outputs_off(); println!("MIXER_PANIC outputs_off {info}"); }));
    if let Err(error) = run() {
        outputs_off();
        println!("MIXER_FATAL outputs_off {error}");
    }
    loop { thread::sleep(Duration::from_secs(1)); }
}
fn run() -> Result<(), Box<dyn std::error::Error>> {
    for pin in PUMPS.into_iter().chain(STIRRERS).chain([HX_CLOCK]) {
        sys::esp!(unsafe { sys::gpio_set_level(pin, 0) })?;
        sys::esp!(unsafe { sys::gpio_set_direction(pin, sys::gpio_mode_t_GPIO_MODE_OUTPUT) })?;
    }
    sys::esp!(unsafe { sys::gpio_set_direction(HX_DOUT, sys::gpio_mode_t_GPIO_MODE_INPUT) })?;
    sys::esp!(unsafe { sys::adc1_config_width(sys::adc_bits_width_t_ADC_WIDTH_BIT_12) })?;
    let channels = [sys::adc1_channel_t_ADC1_CHANNEL_4, sys::adc1_channel_t_ADC1_CHANNEL_5, sys::adc1_channel_t_ADC1_CHANNEL_6];
    for channel in channels {
        sys::esp!(unsafe { sys::adc1_config_channel_atten(channel, sys::adc_atten_t_ADC_ATTEN_DB_11) })?;
    }
    let adc_full: f32 = env!("LEVEL_ADC_FULL").parse()?;
    let counts_per_g: f32 = option_env!("SCALE_COUNTS_PER_G").unwrap_or("1000").parse()?;
    let scale_offset: f32 = option_env!("SCALE_OFFSET_COUNTS").unwrap_or("0").parse()?;
    let port: u16 = env!("MQTT_PORT").parse()?;
    assert!(adc_full > 0.0 && adc_full <= 4095.0);
    assert!(counts_per_g.is_finite() && counts_per_g > 0.0);
    assert!(!DEVICE_ID.is_empty() && DEVICE_ID.len() <= 40 && DEVICE_ID.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_'));
    let session = format!("esp32-{:08x}{:08x}", unsafe { sys::esp_random() }, unsafe { sys::esp_random() });
    let mut device = Device::new(DEVICE_ID.into(), session.clone());
    println!("MIXER_BOOT device={} session={} mqtt={}:{} adc_full={} counts_per_g={} runtime=esp-idf", DEVICE_ID, session, MQTT_HOST, port, adc_full, counts_per_g);
    let peripherals = Peripherals::take()?;
    let event_loop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;
    let mut wifi = EspWifi::new(peripherals.modem, event_loop, Some(nvs))?;
    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: WIFI_SSID.try_into().expect("SSID at most 32 bytes"),
        password: WIFI_PASSWORD.try_into().expect("password at most 64 bytes"),
        auth_method: if WIFI_PASSWORD.is_empty() { AuthMethod::None } else { AuthMethod::WPA2Personal },
        channel: if WIFI_SSID == "Wokwi-GUEST" { Some(6) } else { None },
        ..Default::default()
    }))?;
    wifi.start()?;
    sys::esp!(unsafe { sys::esp_wifi_set_ps(sys::wifi_ps_type_t_WIFI_PS_NONE) })?;
    println!("WIFI_CONNECT {:?}", wifi.connect());

    let mut sensors = Sensors::default();
    let mut have_scale = false;
    let mut last_scale = 0u64;
    let mut adc_ready = 0u8;
    let mut adc_phase = 0usize;
    let mut stream: Option<TcpStream> = None;
    let mut connecting: Option<Receiver<ConnectResult>> = None;
    let mut incoming = Vec::new();
    let mut pending: Option<(Vec<u8>, usize)> = None;
    let mut next_tcp = 0u64;
    let mut next_wifi = millis() + 5000;
    let mut mqtt_deadline = 0u64;
    let mut last_log = 0u64;
    let mut last_state = device.controller.state;
    let mut last_loop = millis();
    let mut previous_wifi = false;
    loop {
        let now = millis();
        if now.saturating_sub(last_loop) > 100 && device.controller.state.active() {
            device.controller.fail("control_deadline", now);
        }
        last_loop = now;
        if let Some(raw) = read_scale() {
            let grams = (raw as f32 - scale_offset) / counts_per_g;
            if grams.is_finite() { sensors.gross_weight_g = grams; have_scale = true; last_scale = now; }
        }
        let raw = unsafe { sys::adc1_get_raw(channels[adc_phase]) };
        if raw >= 0 {
            sensors.levels_pct[adc_phase] = (raw as f32 * 100.0 / adc_full).clamp(0.0, 100.0);
            adc_ready |= 1 << adc_phase;
        } else { device.controller.fail("level_sensor_invalid", now); }
        adc_phase = (adc_phase + 1) % 3;
        sensors.scale_valid = have_scale && now.saturating_sub(last_scale) <= SCALE_STALE_MS && adc_ready == 7;
        let was_connected = device.connected;
        device.tick(now, sensors);
        apply_outputs(&device);
        let wifi_link = wifi.is_connected().unwrap_or(false);
        let wifi_up = wifi_link && wifi.sta_netif().is_up().unwrap_or(false);
        if wifi_up && !previous_wifi {
            println!("WIFI_READY ip={}", wifi.sta_netif().get_ip_info()?.ip);
        }
        previous_wifi = wifi_up;
        let mut lost = was_connected && !device.connected;
        if !wifi_up && (stream.is_some() || device.connected) { lost = true; }
        if !wifi_link && now >= next_wifi {
            next_wifi = now + 5000;
            println!("WIFI_RECONNECT {:?}", wifi.connect());
        }
        if let Some(result) = connecting.as_ref().map(|receiver| receiver.try_recv()) {
            match result {
                Ok(Ok(socket)) if wifi_up => {
                    println!("MQTT_TCP_CONNECTED");
                    stream = Some(socket); connecting = None; incoming.clear();
                    pending = Some((device.connect_packet(), 0)); mqtt_deadline = now + 10_000;
                }
                Ok(Ok(_)) | Err(TryRecvError::Disconnected) => { connecting = None; next_tcp = now + 2000; }
                Ok(Err(error)) => { println!("MQTT_CONNECT_RETRY {error}"); connecting = None; next_tcp = now + 2000; }
                Err(TryRecvError::Empty) => {}
            }
        }
        if wifi_up && stream.is_none() && connecting.is_none() && now >= next_tcp {
            connecting = Some(connect_worker(port)); next_tcp = now + 5000;
        }
        if let Some(socket) = stream.as_mut() {
            let mut bytes = [0u8; 512];
            for _ in 0..4 {
                match socket.read(&mut bytes) {
                    Ok(0) => { lost = true; break; }
                    Ok(n) => { incoming.extend_from_slice(&bytes[..n]); if incoming.len() > mqtt::MAX_PACKET { lost = true; break; } }
                    Err(error) if error.kind() == io::ErrorKind::WouldBlock => break,
                    Err(_) => { lost = true; break; }
                }
            }
        }
        for _ in 0..4 {
            if lost { break; }
            match mqtt::decode(&incoming) {
                Ok(Some((packet, length))) => { device.frame(packet, now); incoming.drain(..length); }
                Ok(None) => break,
                Err(_) => { lost = true; break; }
            }
        }
        if let Some(socket) = stream.as_mut() {
            if !device.connected && now > mqtt_deadline { lost = true; }
            for _ in 0..4 {
                if lost { break; }
                if pending.is_none() { pending = device.pop_packet().map(|packet| (packet, 0)); }
                let Some((packet, offset)) = pending.as_mut() else { break; };
                match socket.write(&packet[*offset..]) {
                    Ok(0) => { lost = true; break; }
                    Ok(n) => { *offset += n; if *offset == packet.len() { pending = None; } }
                    Err(error) if error.kind() == io::ErrorKind::WouldBlock => break,
                    Err(_) => { lost = true; break; }
                }
            }
        }
        if !was_connected && device.connected { println!("MQTT_CONNECTED device={DEVICE_ID}"); }
        if lost {
            device.connection_lost(now); stream = None; pending = None; incoming.clear();
            next_tcp = now + 2000; println!("MQTT_RECONNECT outputs_off");
        }
        apply_outputs(&device);
        if device.controller.state != last_state || now.saturating_sub(last_log) >= 1000 {
            last_state = device.controller.state; last_log = now;
            println!("MIXER state={:?} gross_g={:.2} net_g={:.2} levels={:?} pumps={:?} scale_valid={} fault={:?} wifi={}",
                last_state, sensors.gross_weight_g, device.controller.weight_g(), sensors.levels_pct,
                device.controller.outputs.pumps, sensors.scale_valid, device.controller.fault, wifi_up);
        }
        thread::sleep(Duration::from_millis(2));
    }
}
