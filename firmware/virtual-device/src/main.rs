//! Native plant and MQTT transport for the shared controller.
use mixer_core::{mqtt, Device, Sensors, SCALE_STALE_MS};
use std::{env, io::{self, Read, Write}, net::{TcpStream, ToSocketAddrs}, time::{Duration, Instant, SystemTime, UNIX_EPOCH}};

struct Options { broker: String, device_id: String, speed: f64, fault: String, verbose: bool, duration: Option<f64> }
fn options() -> Options {
    let mut o=Options{broker:"127.0.0.1:1883".into(),device_id:"mixer-01".into(),speed:1.,fault:String::new(),verbose:false,duration:None};
    let mut a=env::args().skip(1);
    while let Some(arg)=a.next(){match arg.as_str(){
        "--broker"=>o.broker=a.next().expect("--broker HOST:PORT"),
        "--device-id"=>o.device_id=a.next().expect("--device-id ID"),
        "--speed"=>o.speed=a.next().expect("--speed NUMBER").parse().expect("valid speed"),
        "--fault"=>o.fault=a.next().expect("--fault no_flow|scale_failure|empty_green"),
        "--verbose"=>o.verbose=true,
        "--duration"=>o.duration=Some(a.next().expect("--duration real-seconds").parse().expect("valid duration")),
        "--help"=>{println!("Native controller test harness (not Wokwi)\n  --broker HOST:PORT --device-id ID --speed N --fault no_flow|scale_failure|empty_green --verbose --duration SECONDS");std::process::exit(0)},
        _=>panic!("unknown option: {arg}")}}
    assert!(o.speed.is_finite()&&(0.1..=50.).contains(&o.speed),"speed0.1..50");
    assert!(!o.device_id.is_empty() && o.device_id.len()<=40 && o.device_id.bytes().all(|b| b.is_ascii_alphanumeric()||b==b'-'||b==b'_'),"invalid device id");
    assert!(["","no_flow","scale_failure","empty_green"].contains(&o.fault.as_str()),"unknown fault");o
}
fn main() -> io::Result<()> {
    let o=options();let started=Instant::now();
    let session=format!("native-{:x}",SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos());
    let mut device=Device::new(o.device_id.clone(),session.clone());
    eprintln!("Native virtual plant (not Wokwi), device={}, session={}, broker={}, speed={}",o.device_id,session,o.broker,o.speed);
    let mut stream:Option<TcpStream>=None;let mut reconnect=Instant::now();
    let mut incoming=Vec::new();let mut pending:Option<(Vec<u8>,usize)>=None;
    let mut sim_now=0u64;let mut sim_budget=0f64;let mut previous=Instant::now();
    let mut gross=120f32;let mut tanks=[1000f32;3];if o.fault=="empty_green"{tanks[0]=0.;}
    let mut sample=Sensors{gross_weight_g:gross,levels_pct:tanks.map(|g|g/10.),scale_valid:true};
    let mut last_sample=0;let mut last_scale_good=0;
    loop {
        let instant=Instant::now();let elapsed=instant.duration_since(previous).as_secs_f64();previous=instant;
        sim_budget+=elapsed.min(0.25)*1000.*o.speed;
        if let Some(duration)=o.duration{if started.elapsed().as_secs_f64()>=duration{break;}}
        if stream.is_none()&&instant>=reconnect {
            let address=o.broker.to_socket_addrs()?.next().ok_or_else(||io::Error::other("broker resolved no addresses"))?;
            match TcpStream::connect_timeout(&address,Duration::from_millis(250)){
                Ok(s)=>{s.set_nonblocking(true)?;s.set_nodelay(true)?;stream=Some(s);incoming.clear();pending=Some((device.connect_packet(),0));eprintln!("TCP connected");},
                Err(e)=>{eprintln!("broker unavailable: {e}");reconnect=instant+Duration::from_secs(2);}
            }
        }
        let mut disconnected=false;
        if let Some(s)=stream.as_mut(){
            let mut bytes=[0u8;1024];
            loop {match s.read(&mut bytes){
                Ok(0)=>{disconnected=true;break},
                Ok(n)=>{incoming.extend_from_slice(&bytes[..n]);if incoming.len()>mqtt::MAX_PACKET{disconnected=true;break;}},
                Err(e) if e.kind()==io::ErrorKind::WouldBlock=>break,
                Err(_)=>{disconnected=true;break}
            }}
            while !disconnected{
                match mqtt::decode(&incoming){
                    Ok(Some((packet,n)))=>{device.frame(packet,sim_now);incoming.drain(..n);},
                    Ok(None)=>break,
                    Err(e)=>{eprintln!("MQTT decode: {e}");disconnected=true;}
                }
            }
        }
        // Ten-millisecond integration preserves pulse timing at every --speed value.
        while sim_budget>=10. {
            sim_budget-=10.;sim_now+=10;
            for i in 0..3 {let amount=if o.fault=="no_flow"{0.}else{device.controller.outputs.pumps[i]*20.*0.01};let amount=amount.min(tanks[i]);tanks[i]-=amount;gross+=amount;}
            if sim_now-last_sample>=100{
                last_sample=sim_now;
                if o.fault!="scale_failure" || sim_now<4500 {sample.gross_weight_g=gross;last_scale_good=sim_now;}
                sample.levels_pct=tanks.map(|g|g/10.);
            }
            sample.scale_valid=sim_now-last_scale_good<=SCALE_STALE_MS;
            device.tick(sim_now,sample);
        }
        if let Some(s)=stream.as_mut(){
            for _ in 0..32 {
                if pending.is_none(){
                    pending=device.pop_packet().map(|p|{
                        if let Ok(Some((mqtt::Packet::Publish{topic,payload,..},_)))=mqtt::decode(&p){
                            if topic.ends_with("/event")||o.verbose{println!("{} {}",topic,String::from_utf8_lossy(payload));}
                        }
                        (p,0)
                    });
                }
                let Some((packet,offset))=pending.as_mut() else{break};
                match s.write(&packet[*offset..]){
                    Ok(0)=>{disconnected=true;break},
                    Ok(n)=>{*offset+=n;if *offset==packet.len(){pending=None;}},
                    Err(e)if e.kind()==io::ErrorKind::WouldBlock=>break,
                    Err(_)=>{disconnected=true;break}
                }
            }
        }
        if disconnected {
            device.connection_lost(sim_now);stream=None;pending=None;incoming.clear();reconnect=Instant::now()+Duration::from_secs(1);eprintln!("network lost; outputs safe");
        }
        std::thread::sleep(Duration::from_millis(2));
    }
    device.controller.command(mixer_core::Command{id:"native-shutdown".into(),session_id:None,expires_uptime_ms:None,kind:mixer_core::CommandKind::Stop},sim_now);
    if let Some(s)=stream.as_mut(){let _=s.write_all(&device.shutdown_packet());let _=s.write_all(&mqtt::disconnect());}
    Ok(())
}
