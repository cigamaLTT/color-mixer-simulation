use alloc::{collections::VecDeque, format, string::String, vec::Vec};
use serde::Serialize;
use crate::{mqtt::{self, Packet}, Command, CommandKind, Controller, Sensors, State};

#[derive(Serialize)]
struct Status<'a> { device_id: &'a str, online: bool }
#[derive(Serialize)]
struct Telemetry<'a> {
    schema_version: u8, device_id: &'a str, session_id: &'a str, seq: u64, uptime_ms: u64,
    state: State, channel: Option<usize>, weight_g: f32, gross_weight_g: f32, target_g: f32,
    levels_pct: [f32;3], pumps: [f32;3], stirrers: [bool;4], delivered_g: [f32;3], scale_valid: bool, fault: Option<&'static str>,
}
/// Shared controller state and MQTT protocol for hardware and native transports.
pub struct Device {
    pub controller: Controller,
    pub device_id: String,
    pub session_id: String,
    pub connected: bool,
    seq: u64,
    last_telemetry: u64,
    last_ping: u64,
    last_rx: u64,
    outgoing: VecDeque<Vec<u8>>,
}
impl Device {
    pub fn new(device_id: String,session_id:String)->Self {
        Self{controller:Controller::new(),device_id,session_id,connected:false,seq:0,last_telemetry:0,last_ping:0,last_rx:0,outgoing:VecDeque::new()}
    }
    pub fn topic(&self,suffix:&str)->String {format!("mixer/{}/{}",self.device_id,suffix)}
    fn status(&self,online:bool)->Vec<u8>{serde_json::to_vec(&Status{device_id:&self.device_id,online}).unwrap()}
    pub fn connect_packet(&self)->Vec<u8>{mqtt::connect(&format!("{}-{}",self.device_id,self.session_id),&self.topic("status"),&self.status(false))}
    fn enqueue(&mut self,packet:Vec<u8>){if self.outgoing.len()>=24{self.outgoing.pop_front();}self.outgoing.push_back(packet);}
    pub fn pop_packet(&mut self)->Option<Vec<u8>>{self.outgoing.pop_front()}
    pub fn connection_lost(&mut self,now:u64){
        self.connected=false;self.outgoing.clear();
        if self.controller.state.active(){self.controller.fail("network_lost",now);}
    }
    pub fn shutdown_packet(&self)->Vec<u8>{mqtt::publish(&self.topic("status"),&self.status(false),true)}
    pub fn telemetry_json(&mut self,now:u64)->Vec<u8>{
        self.seq=self.seq.wrapping_add(1);
        let c=&self.controller;
        serde_json::to_vec(&Telemetry{schema_version:1,device_id:&self.device_id,session_id:&self.session_id,seq:self.seq,uptime_ms:now,
            state:c.state,channel:c.channel,weight_g:c.weight_g(),gross_weight_g:c.sensors.gross_weight_g,target_g:c.target_g,
            levels_pct:c.sensors.levels_pct,pumps:c.outputs.pumps,stirrers:c.outputs.stirrers,delivered_g:c.delivered_g,scale_valid:c.sensors.scale_valid,fault:c.fault}).unwrap()
    }
    pub fn command_payload(&mut self,payload:&[u8],retained:bool,now:u64){
        if payload.len()>1024{self.controller.reject(None,"command_too_large");return;}
        let command:Command=match serde_json::from_slice(payload){Ok(c)=>c,Err(_)=>{self.controller.reject(None,"invalid_command");return;}};
        // Stop is an unconditional fail-safe, including when old clients have stale session data.
        if !matches!(command.kind,CommandKind::Stop){
            if retained{self.controller.reject(Some(command.id),"retained_command");return;}
            if command.session_id.as_deref()!=Some(self.session_id.as_str()){self.controller.reject(Some(command.id),"session_mismatch");return;}
            match command.expires_uptime_ms{Some(expiry) if expiry>=now && expiry<=now.saturating_add(60_000)=>{},_=>{self.controller.reject(Some(command.id),"command_expired");return;}}
        }
        self.controller.command(command,now);
    }
    pub fn frame(&mut self,packet:Packet<'_>,now:u64){
        self.last_rx=now;
        match packet{
            Packet::ConnAck{code:0}=>{
                self.connected=true;self.last_ping=now;self.last_telemetry=now.saturating_sub(500);
                self.enqueue(mqtt::subscribe(&self.topic("command"),1));
                self.enqueue(mqtt::publish(&self.topic("status"),&self.status(true),true));
            }
            Packet::ConnAck{..}=>self.connection_lost(now),
            Packet::Publish{topic,payload,retained,id,..}=>{
                if let Some(id)=id{self.enqueue(mqtt::puback(id));}
                if self.connected && topic==self.topic("command"){self.command_payload(payload,retained,now);}
            }
            _=>{}
        }
    }
    pub fn tick(&mut self,now:u64,sensors:Sensors){
        self.controller.tick(now,sensors);
        if self.connected && now.saturating_sub(self.last_rx)>30_000{self.connection_lost(now);}
        if !self.connected{return;}
        while let Some(e)=self.controller.pop_event(){
            if let Ok(json)=serde_json::to_vec(&e){self.enqueue(mqtt::publish(&self.topic("event"),&json,false));}
        }
        if now.saturating_sub(self.last_telemetry)>=250{
            self.last_telemetry=now;let json=self.telemetry_json(now);self.enqueue(mqtt::publish(&self.topic("telemetry"),&json,false));
        }
        if now.saturating_sub(self.last_ping)>=10_000{self.last_ping=now;self.enqueue(mqtt::ping());}
    }
}
#[cfg(test)]
mod tests{
    use super::*;
    use alloc::string::ToString;
    #[test]
    fn runtime_rejects_old_retained_and_expired_start_but_always_stops(){
        let mut d=Device::new("mixer-01".to_string(),"session-new".to_string());
        d.controller.tick(0,Sensors{gross_weight_g:120.,levels_pct:[100.;3],scale_valid:true});
        for (json,retained) in [
            (r#"{"id":"old","type":"start","session_id":"session-old","expires_uptime_ms":5000,"total_g":10,"ratios":[100,0,0]}"#,false),
            (r#"{"id":"retained","type":"start","session_id":"session-new","expires_uptime_ms":5000,"total_g":10,"ratios":[100,0,0]}"#,true),
            (r#"{"id":"expired","type":"start","session_id":"session-new","expires_uptime_ms":0,"total_g":10,"ratios":[100,0,0]}"#,false),
        ]{d.command_payload(json.as_bytes(),retained,100);assert_eq!(d.controller.state,State::Idle);}
        d.command_payload(br#"{"id":"s","type":"stop"}"#,true,200);assert_eq!(d.controller.state,State::Stopped);
    }
}
