#![no_std]
#![no_main]
extern crate alloc;

use alloc::{format, string::ToString, vec::Vec};
use core::net::Ipv4Addr;
use esp_backtrace as _;
use esp_hal::{
    analog::adc::{Adc, AdcConfig, Attenuation}, clock::CpuClock, delay::Delay,
    gpio::{Input, Level, Output, Pull}, main, rng::Rng, time,
    timer::timg::{TimerGroup, MwdtStage},
};
use esp_println::println;
use fugit::ExtU64;
use esp_wifi::{config::PowerSaveMode, init, wifi::{utils::create_network_interface, AuthMethod, ClientConfiguration, Configuration, WifiStaDevice}};
use mixer_core::{mqtt, Device, Sensors, SCALE_STALE_MS};
use smoltcp::{
    iface::{SocketSet, SocketStorage}, socket::{dhcpv4, dns, tcp},
    time::{Duration as NetDuration, Instant as NetInstant}, wire::{DnsQueryType, IpAddress, IpCidr},
};

const WIFI_SSID: &str = env!("WIFI_SSID");
const WIFI_PASSWORD: &str = env!("WIFI_PASSWORD");
const MQTT_HOST: &str = env!("MQTT_HOST");
const DEVICE_ID: &str = env!("DEVICE_ID");
const OUTPUT_MASK: u32 = (1<<18)|(1<<19)|(1<<21)|(1<<22)|(1<<23)|(1<<25)|(1<<26);

#[panic_handler]
fn panic(info: &core::panic::PanicInfo) -> ! {
    // This runs even if allocation, the radio, or a GPIO driver panics mid-dose.
    unsafe { &*esp32::GPIO::ptr() }.out_w1tc().write(|w| unsafe { w.bits(OUTPUT_MASK) });
    println!("MIXER_PANIC outputs_off {}", info);
    loop { core::hint::spin_loop(); }
}
fn millis() -> u64 { time::now().duration_since_epoch().to_millis() }
fn read_scale(dout: &Input<'_>, clock: &mut Output<'_>, delay: &Delay) -> Option<i32> {
    if dout.is_high() { return None; }
    // HX711 powers down when SCK stays high >60us; prevent Wi-Fi interrupts
    // from lengthening any of these 2us high pulses.
    Some(critical_section::with(|_| {
        let mut raw=0u32;
        for _ in 0..24 {
            clock.set_high();delay.delay_micros(2);
            raw=(raw<<1)|u32::from(dout.is_high());
            clock.set_low();delay.delay_micros(2);
        }
        clock.set_high();delay.delay_micros(2);clock.set_low();
        if raw&0x80_0000!=0 {(raw|0xff00_0000) as i32}else{raw as i32}
    }))
}
#[main]
fn main() -> ! {
    let peripherals=esp_hal::init(esp_hal::Config::default().with_cpu_clock(CpuClock::max()));
    // Active-high MOSFET/relay drivers require external gate pull-downs too.
    let mut pumps=[Output::new(peripherals.GPIO18,Level::Low),Output::new(peripherals.GPIO19,Level::Low),Output::new(peripherals.GPIO21,Level::Low)];
    let mut stirrers=[Output::new(peripherals.GPIO22,Level::Low),Output::new(peripherals.GPIO23,Level::Low),Output::new(peripherals.GPIO25,Level::Low),Output::new(peripherals.GPIO26,Level::Low)];
    let dout=Input::new(peripherals.GPIO35,Pull::None);
    let mut scale_clock=Output::new(peripherals.GPIO27,Level::Low);
    let delay=Delay::new();
    esp_alloc::heap_allocator!(96 * 1024);
    let mut adc_config=AdcConfig::new();
    let mut green=adc_config.enable_pin(peripherals.GPIO32,Attenuation::_11dB);
    let mut skin=adc_config.enable_pin(peripherals.GPIO33,Attenuation::_11dB);
    let mut blue=adc_config.enable_pin(peripherals.GPIO34,Attenuation::_11dB);
    let mut adc=Adc::new(peripherals.ADC1,adc_config);
    let adc_full: f32=env!("LEVEL_ADC_FULL").parse().expect("LEVEL_ADC_FULL numeric");
    assert!(adc_full>0.0&&adc_full<=4095.0);
    let counts_per_g: f32=option_env!("SCALE_COUNTS_PER_G").unwrap_or("1000").parse().unwrap();
    let scale_offset: f32=option_env!("SCALE_OFFSET_COUNTS").unwrap_or("0").parse().unwrap();
    assert!(counts_per_g.is_finite()&&counts_per_g>0.0);
    let port:u16=env!("MQTT_PORT").parse().expect("MQTT_PORT numeric");
    assert!(DEVICE_ID.len()<=40&&!DEVICE_ID.is_empty()&&DEVICE_ID.bytes().all(|b|b.is_ascii_alphanumeric()||b==b'-'||b==b'_'));
    let timg0=TimerGroup::new(peripherals.TIMG0);
    let mut rng=Rng::new(peripherals.RNG);
    let session=format!("esp32-{:08x}{:08x}",rng.random(),rng.random());
    let mut device=Device::new(DEVICE_ID.to_string(),session.clone());
    println!("MIXER_BOOT device={} session={} mqtt={}:{} adc_full={} counts_per_g={}",DEVICE_ID,session,MQTT_HOST,port,adc_full,counts_per_g);
    let wifi_init=init(timg0.timer0,rng,peripherals.RADIO_CLK).expect("WiFi init");
    let (mut iface,mut radio,mut wifi)=create_network_interface(&wifi_init,peripherals.WIFI,WifiStaDevice).expect("WiFi interface");
    wifi.set_configuration(&Configuration::Client(ClientConfiguration{
        ssid:WIFI_SSID.try_into().expect("SSID at most32bytes"),
        auth_method:if WIFI_PASSWORD.is_empty(){AuthMethod::None}else{AuthMethod::WPA2Personal},
        // Wokwi's built-in access point always uses channel 6. Supplying it
        // avoids a full scan before every reconnect in the simulator.
        channel:if WIFI_SSID=="Wokwi-GUEST"{Some(6)}else{None},
        password:WIFI_PASSWORD.try_into().expect("password at most64bytes"),..Default::default()
    })).expect("WiFi configuration");
    wifi.start().expect("WiFi start");
    wifi.set_power_saving(PowerSaveMode::None).expect("WiFi power saving");
    println!("WIFI_CONNECT {:?}",wifi.connect());

    let mut storage:[SocketStorage;3]=Default::default();
    let mut sockets=SocketSet::new(&mut storage[..]);
    let dhcp_handle=sockets.add(dhcpv4::Socket::new());
    let mut queries:[Option<dns::DnsQuery>;1]=[None];
    let dns_handle=sockets.add(dns::Socket::new(&[],&mut queries[..]));
    let mut rx=[0u8;4096];let mut tx=[0u8;8192];
    let mut tcp_socket=tcp::Socket::new(tcp::SocketBuffer::new(&mut rx[..]),tcp::SocketBuffer::new(&mut tx[..]));
    tcp_socket.set_timeout(Some(NetDuration::from_secs(5)));
    tcp_socket.set_keep_alive(Some(NetDuration::from_secs(5)));
    let tcp_handle=sockets.add(tcp_socket);
    let mut sensors=Sensors::default();let mut last_scale=0u64;let mut have_scale=false;
    let mut adc_phase=0u8;let mut adc_ready=0u8;
    let mut configured=false;let mut broker_ip=MQTT_HOST.parse::<Ipv4Addr>().ok().map(IpAddress::Ipv4);
    let mut dns_query=None;let mut next_dns=0u64;
    let mut next_wifi=5000u64;let mut next_tcp=0u64;let mut tcp_deadline=0u64;let mut mqtt_sent=false;
    let mut incoming=Vec::new();let mut pending:Option<(Vec<u8>,usize)>=None;
    let mut last_log=0u64;let mut last_state=device.controller.state;let mut last_loop=millis();
    let mut watchdog=TimerGroup::new(peripherals.TIMG1).wdt;
    watchdog.set_timeout(MwdtStage::Stage0,250_000u64.micros());
    watchdog.enable();
    loop {
        let now=millis();
        // A missed control deadline must never leave a running pump energized.
        if now.saturating_sub(last_loop)>100&&device.controller.state.active(){device.controller.fail("control_deadline",now);}
        last_loop=now;
        if let Some(raw)=read_scale(&dout,&mut scale_clock,&delay){
            let grams=(raw as f32-scale_offset)/counts_per_g;
            if grams.is_finite(){sensors.gross_weight_g=grams;last_scale=now;have_scale=true;}
        }
        // ADC1 conversions are polled once per loop, never waited on.
        let reading=match adc_phase {
            0=>adc.read_oneshot(&mut green),1=>adc.read_oneshot(&mut skin),_=>adc.read_oneshot(&mut blue),
        };
        match reading {
            Ok(raw)=>{let i=adc_phase as usize;sensors.levels_pct[i]=(raw as f32*100.0/adc_full).clamp(0.,100.);adc_ready|=1<<adc_phase;adc_phase=(adc_phase+1)%3;},
            Err(nb::Error::WouldBlock)=>{},
            Err(nb::Error::Other(_))=>{device.controller.fail("level_sensor_invalid",now);},
        }
        sensors.scale_valid=have_scale&&now.saturating_sub(last_scale)<=SCALE_STALE_MS&&adc_ready==7;
        let was_connected=device.connected;
        device.tick(now,sensors);
        // Apply safety/control output before any network processing.
        for i in 0..3{pumps[i].set_level(Level::from(device.controller.outputs.pumps[i]>0.0));}
        for i in 0..4{stirrers[i].set_level(Level::from(device.controller.outputs.stirrers[i]));}
        if was_connected&&!device.connected{sockets.get_mut::<tcp::Socket>(tcp_handle).abort();mqtt_sent=false;pending=None;incoming.clear();next_tcp=now+2000;}

        let wifi_up=wifi.is_connected().unwrap_or(false);
        if !wifi_up {
            if mqtt_sent||device.connected{device.connection_lost(now);sockets.get_mut::<tcp::Socket>(tcp_handle).abort();mqtt_sent=false;pending=None;incoming.clear();}
            if now>=next_wifi{next_wifi=now+5000;println!("WIFI_RECONNECT state={:?} result={:?}",esp_wifi::wifi::sta_state(),wifi.connect());}
        }
        let timestamp=NetInstant::from_millis(now as i64);
        // At most four inbound Ethernet frames per turn. smoltcp::Interface::poll()
        // would drain the entire receive queue and could delay pump shutoff.
        for _ in 0..4{iface.poll_ingress_single(timestamp,&mut radio,&mut sockets);}
        iface.poll_egress(timestamp,&mut radio,&mut sockets);
        let update=sockets.get_mut::<dhcpv4::Socket>(dhcp_handle).poll().map(|event|match event{
            dhcpv4::Event::Configured(c)=>Some((c.address,c.router,c.dns_servers)),dhcpv4::Event::Deconfigured=>None,
        });
        if let Some(update)=update{match update{
            Some((address,router,servers))=>{
                iface.update_ip_addrs(|a|{a.clear();let _=a.push(IpCidr::Ipv4(address));});
                if let Some(router)=router{let _=iface.routes_mut().add_default_ipv4_route(router);}
                let servers:Vec<IpAddress>=servers.iter().copied().map(IpAddress::Ipv4).collect();
                sockets.get_mut::<dns::Socket>(dns_handle).update_servers(&servers);
                configured=true;println!("WIFI_READY ip={}",address);
            }
            None=>{
                configured=false;iface.update_ip_addrs(|a|a.clear());iface.routes_mut().remove_default_ipv4_route();
                device.connection_lost(now);sockets.get_mut::<tcp::Socket>(tcp_handle).abort();mqtt_sent=false;pending=None;incoming.clear();
            }
        }}
        if configured&&broker_ip.is_none()&&dns_query.is_none()&&now>=next_dns{
            match sockets.get_mut::<dns::Socket>(dns_handle).start_query(iface.context(),MQTT_HOST,DnsQueryType::A){
                Ok(query)=>dns_query=Some(query),Err(_)=>next_dns=now+5000,
            }
        }
        if let Some(query)=dns_query{
            match sockets.get_mut::<dns::Socket>(dns_handle).get_query_result(query){
                Ok(addresses)=>{dns_query=None;broker_ip=addresses.first().copied();next_dns=now+5000;if let Some(ip)=broker_ip{println!("MQTT_DNS {}={}",MQTT_HOST,ip);}},
                Err(dns::GetQueryResultError::Pending)=>{},Err(_)=>{dns_query=None;next_dns=now+5000;println!("MQTT_DNS_RETRY");},
            }
        }
        let mut lost=false;
        {
            let socket=sockets.get_mut::<tcp::Socket>(tcp_handle);
            if configured&&wifi_up&&!socket.is_open()&&now>=next_tcp{
                if let Some(ip)=broker_ip{
                    let local_port=49152+(now%16000) as u16;
                    if socket.connect(iface.context(),(ip,port),local_port).is_ok(){tcp_deadline=now+10_000;mqtt_sent=false;next_tcp=now+5000;}
                }
            }
            if socket.state()==tcp::State::Established&&!mqtt_sent{pending=Some((device.connect_packet(),0));mqtt_sent=true;tcp_deadline=now+10_000;}
            if mqtt_sent&&!socket.is_active(){lost=true;}
            if socket.is_open()&&!device.connected&&now>tcp_deadline{socket.abort();lost=true;}
            let mut bytes=[0u8;512];
            for _ in 0..4{
                if !socket.can_recv(){break;}
                match socket.recv_slice(&mut bytes){Ok(n)=>{incoming.extend_from_slice(&bytes[..n]);if incoming.len()>mqtt::MAX_PACKET{lost=true;break;}},Err(_)=>{lost=true;break;}}
            }
        }
        // Bounded parsing; MAX_PACKET also limits JSON command allocation.
        for _ in 0..4{
            match mqtt::decode(&incoming){
                Ok(Some((packet,len)))=>{device.frame(packet,now);incoming.drain(..len);},
                Ok(None)=>break,Err(_)=>{lost=true;break;}
            }
        }
        {
            let socket=sockets.get_mut::<tcp::Socket>(tcp_handle);
            for _ in 0..4{
                if !socket.can_send(){break;}
                if pending.is_none(){pending=device.pop_packet().map(|p|(p,0));}
                let Some((packet,offset))=pending.as_mut()else{break};
                match socket.send_slice(&packet[*offset..]){Ok(n)=>{*offset+=n;if *offset==packet.len(){pending=None;}},Err(_)=>{lost=true;break;}}
            }
        }
        if !was_connected&&device.connected{println!("MQTT_CONNECTED device={}",DEVICE_ID);}
        if lost{device.connection_lost(now);sockets.get_mut::<tcp::Socket>(tcp_handle).abort();mqtt_sent=false;pending=None;incoming.clear();next_tcp=now+2000;println!("MQTT_RECONNECT outputs_off");}
        // Commands and disconnects can change outputs during network processing.
        for i in 0..3{pumps[i].set_level(Level::from(device.controller.outputs.pumps[i]>0.0));}
        for i in 0..4{stirrers[i].set_level(Level::from(device.controller.outputs.stirrers[i]));}
        if device.controller.state!=last_state||now.saturating_sub(last_log)>=1000{
            last_state=device.controller.state;last_log=now;
            println!("MIXER state={:?} gross_g={:.2} net_g={:.2} levels={:?} pumps={:?} scale_valid={} fault={:?} wifi={:?}",last_state,sensors.gross_weight_g,device.controller.weight_g(),sensors.levels_pct,device.controller.outputs.pumps,sensors.scale_valid,device.controller.fault,esp_wifi::wifi::sta_state());
        }
        watchdog.feed();
        delay.delay_millis(2);
    }
}
