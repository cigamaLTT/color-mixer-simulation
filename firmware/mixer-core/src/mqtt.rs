//! MQTT 3.1.1 framing. Transports never block the controller while waiting for a packet.
use alloc::{vec, vec::Vec};
use core::str;

pub const MAX_PACKET: usize = 2048;
#[derive(Debug, PartialEq)]
pub enum Packet<'a> {
    ConnAck { code: u8 },
    Publish { topic: &'a str, payload: &'a [u8], retained: bool, qos: u8, id: Option<u16> },
    SubAck,
    PubAck,
    PingResp,
    Other,
}
fn field(out: &mut Vec<u8>, bytes: &[u8]) { out.extend_from_slice(&(bytes.len() as u16).to_be_bytes()); out.extend_from_slice(bytes); }
fn wrap(header: u8, body: Vec<u8>) -> Vec<u8> {
    let mut out = vec![header]; let mut len = body.len();
    loop { let mut byte = (len % 128) as u8; len /= 128; if len > 0 { byte |= 0x80; } out.push(byte); if len == 0 { break; } }
    out.extend(body); out
}
pub fn connect(client_id: &str, will_topic: &str, will_payload: &[u8]) -> Vec<u8> {
    let mut body = Vec::new(); field(&mut body,b"MQTT");
    body.extend_from_slice(&[4,0x26,0,20]); // clean session, retained LWT, 20-second keepalive
    field(&mut body,client_id.as_bytes()); field(&mut body,will_topic.as_bytes()); field(&mut body,will_payload);
    wrap(0x10,body)
}
pub fn subscribe(topic: &str, id: u16) -> Vec<u8> {
    let mut body=id.to_be_bytes().to_vec();field(&mut body,topic.as_bytes());body.push(1);wrap(0x82,body)
}
pub fn publish(topic: &str, payload: &[u8], retained: bool) -> Vec<u8> {
    let mut body=Vec::new();field(&mut body,topic.as_bytes());body.extend_from_slice(payload);wrap(if retained {0x31}else{0x30},body)
}
pub fn puback(id: u16) -> Vec<u8> { let [hi,lo]=id.to_be_bytes();vec![0x40,2,hi,lo] }
pub fn ping() -> Vec<u8> { vec![0xc0,0] }
pub fn disconnect() -> Vec<u8> { vec![0xe0,0] }

pub fn decode(input: &[u8]) -> Result<Option<(Packet<'_>,usize)>, &'static str> {
    if input.len()<2 { return Ok(None); }
    let mut remaining=0usize;let mut multiplier=1;let mut pos=1;
    loop {
        if pos>=input.len(){ return Ok(None); }
        let b=input[pos]; pos+=1;remaining+=(b as usize&127)*multiplier;
        if remaining>MAX_PACKET { return Err("mqtt_packet_too_large"); }
        if b&128==0 {break;}
        if pos>=5 {return Err("mqtt_invalid_length");}multiplier*=128;
    }
    if remaining+pos>MAX_PACKET {return Err("mqtt_packet_too_large");}
    let end=pos+remaining;
    if input.len()<end{return Ok(None);}
    let body=&input[pos..end];
    let packet=match input[0]>>4 {
        2=>{if body.len()!=2{return Err("mqtt_invalid_connack");}Packet::ConnAck{code:body[1]}},
        3=>{
            if body.len()<2{return Err("mqtt_invalid_publish");}
            let n=u16::from_be_bytes([body[0],body[1]]) as usize;
            if n==0 || n+2>body.len(){return Err("mqtt_invalid_topic");}
            let topic=str::from_utf8(&body[2..n+2]).map_err(|_|"mqtt_invalid_topic")?;
            let qos=(input[0]>>1)&3;if qos>1{return Err("mqtt_unsupported_qos");}
            let mut p=n+2;
            let id=if qos==1 {if p+2>body.len(){return Err("mqtt_invalid_packet_id");}let id=u16::from_be_bytes([body[p],body[p+1]]);p+=2;Some(id)} else {None};
            Packet::Publish{topic,payload:&body[p..],retained:input[0]&1!=0,qos,id}
        },
        4=>Packet::PubAck,
        9=>Packet::SubAck,
        13=>Packet::PingResp,
        _=>Packet::Other,
    };
    Ok(Some((packet,end)))
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn publish_frames_survive_fragmentation_and_concatenation() {
        let data=publish("mixer/01/command",br#"{"type":"stop"}"#,false);
        for n in 0..data.len(){assert_eq!(decode(&data[..n]).unwrap(),None);}
        match decode(&data).unwrap().unwrap(){(Packet::Publish{topic,payload,retained,..},len)=>{assert_eq!(topic,"mixer/01/command");assert_eq!(payload,br#"{"type":"stop"}"#);assert!(!retained);assert_eq!(len,data.len());},_=>panic!()}
    }
    #[test]
    fn rejects_oversized_or_overlong_remaining_length() {assert!(decode(&[0x30,255,255,255,255,0]).is_err());}
}
