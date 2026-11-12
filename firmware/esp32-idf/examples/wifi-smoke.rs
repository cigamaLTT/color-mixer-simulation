//! Minimal classic ESP32 WiFi and DHCP check for Wokwi.
//!
//! Based on the esp-idf-svc v0.51.0 blocking WiFi example:
//! https://github.com/esp-rs/esp-idf-svc/blob/v0.51.0/examples/wifi.rs

use esp_idf_svc::{
    eventloop::EspSystemEventLoop,
    hal::prelude::Peripherals,
    log::EspLogger,
    nvs::EspDefaultNvsPartition,
    wifi::{AuthMethod, BlockingWifi, ClientConfiguration, Configuration, EspWifi},
};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    esp_idf_svc::sys::link_patches();
    EspLogger::initialize_default();

    let peripherals = Peripherals::take()?;
    let sys_loop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;
    let mut wifi = BlockingWifi::wrap(
        EspWifi::new(peripherals.modem, sys_loop.clone(), Some(nvs))?,
        sys_loop,
    )?;

    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: "Wokwi-GUEST".try_into().unwrap(),
        auth_method: AuthMethod::None,
        channel: Some(6),
        ..Default::default()
    }))?;

    println!("WIFI_START ssid=Wokwi-GUEST channel=6");
    wifi.start()?;
    wifi.connect()?;
    wifi.wait_netif_up()?;

    let ip = wifi.wifi().sta_netif().get_ip_info()?;
    println!(
        "WIFI_CONNECTED ip={} gateway={} subnet={}",
        ip.ip, ip.subnet.gateway, ip.subnet.mask
    );

    // Keep the driver and its network interface alive after DHCP completes.
    loop {
        std::thread::sleep(std::time::Duration::from_secs(30));
    }
}
