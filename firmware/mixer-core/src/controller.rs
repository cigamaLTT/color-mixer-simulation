use alloc::{collections::VecDeque, string::String};
use serde::{Deserialize, Serialize};

pub const MAX_GROSS_G: f32 = 1000.0;
pub const CAPACITY_RESERVE_G: f32 = 5.0;
pub const TANK_CAPACITY_G: f32 = 1000.0;
pub const SCALE_STALE_MS: u64 = 1000;
const START_STIR_MS: u64 = 2000;
const TARE_SETTLE_MS: u64 = 1000;
const CHANNEL_SETTLE_MS: u64 = 700;
const FINAL_MIX_MS: u64 = 3000;
const DOSE_TIMEOUT_MS: u64 = 120_000;
const NO_FLOW_MS: u64 = 6000;
const DOSE_TOLERANCE_G: f32 = 0.25;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum State { Idle, Stirring, Taring, Dosing, Settling, Mixing, Complete, Stopped, Fault }
impl State {
    pub fn active(self) -> bool { matches!(self, Self::Stirring | Self::Taring | Self::Dosing | Self::Settling | Self::Mixing) }
}

#[derive(Debug, Clone, Deserialize)]
pub struct Command {
    pub id: String,
    #[serde(default)]
    pub session_id: Option<String>,
    #[serde(default)]
    pub expires_uptime_ms: Option<u64>,
    #[serde(flatten)]
    pub kind: CommandKind,
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum CommandKind { Start { total_g: f32, ratios: [f32; 3] }, Stop, Reset, Tare }

#[derive(Debug, Clone, Copy)]
pub struct Sensors { pub gross_weight_g: f32, pub levels_pct: [f32; 3], pub scale_valid: bool }
impl Default for Sensors {
    fn default() -> Self { Self { gross_weight_g: 0.0, levels_pct: [100.0; 3], scale_valid: false } }
}
#[derive(Debug, Clone, Copy, Default, Serialize)]
pub struct Outputs { pub pumps: [f32; 3], pub stirrers: [bool; 4] }

#[derive(Debug, Clone, Serialize)]
pub struct Event {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
    #[serde(rename = "type")]
    pub event_type: &'static str,
    pub state: State,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub accepted: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fault: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub delivered_g: Option<[f32; 3]>,
    #[serde(skip_serializing_if = "core::ops::Not::not")]
    pub duplicate: bool,
}

#[derive(Debug)]
pub struct Controller {
    pub state: State,
    pub channel: Option<usize>,
    pub outputs: Outputs,
    pub sensors: Sensors,
    pub delivered_g: [f32; 3],
    pub target_g: f32,
    pub fault: Option<&'static str>,
    tare_g: f32,
    targets: [f32; 3],
    batch_pending: bool,
    state_since: u64,
    batch_since: u64,
    dose_since: u64,
    tare_stable_since: u64,
    stable_g: f32,
    channel_start_g: f32,
    flow_g: f32,
    flow_since: u64,
    events: VecDeque<Event>,
    commands: VecDeque<(String, bool, Option<&'static str>)>,
}
impl Default for Controller { fn default() -> Self { Self::new() } }
impl Controller {
    pub fn new() -> Self {
        Self { state: State::Idle, channel: None, outputs: Outputs::default(), sensors: Sensors::default(),
            delivered_g: [0.0; 3], target_g: 0.0, fault: None, tare_g: 0.0, targets: [0.0; 3],
            batch_pending: false, state_since: 0, batch_since: 0, dose_since: 0, tare_stable_since: 0,
            stable_g: 0.0, channel_start_g: 0.0, flow_g: 0.0, flow_since: 0,
            events: VecDeque::new(), commands: VecDeque::new() }
    }
    pub fn weight_g(&self) -> f32 { (self.sensors.gross_weight_g - self.tare_g).max(0.0) }
    fn push(&mut self, event: Event) {
        if self.events.len() >= 32 { self.events.pop_front(); }
        self.events.push_back(event);
    }
    pub fn pop_event(&mut self) -> Option<Event> { self.events.pop_front() }
    fn event(&self, event_type: &'static str) -> Event {
        Event { id: None, event_type, state: self.state, accepted: None, reason: None,
            fault: None, delivered_g: None, duplicate: false }
    }
    fn transition(&mut self, state: State, now: u64) {
        self.state = state;
        self.state_since = now;
        self.outputs = Outputs::default();
        self.push(self.event("state_changed"));
    }
    pub fn fail(&mut self, reason: &'static str, now: u64) {
        if self.state == State::Fault { return; }
        self.fault = Some(reason);
        self.channel = None;
        self.batch_pending = false;
        self.transition(State::Fault, now);
        let mut e = self.event("fault"); e.fault = Some(reason); self.push(e);
    }
    pub fn reject(&mut self, id: Option<String>, reason: &'static str) {
        let mut e = self.event("command_ack"); e.id = id; e.accepted = Some(false); e.reason = Some(reason); self.push(e);
    }
    pub fn command(&mut self, command: Command, now: u64) {
        if let Some((_, accepted, reason)) = self.commands.iter().find(|(id, _, _)| id == &command.id).cloned() {
            if matches!(command.kind, CommandKind::Stop) { let _ = self.apply(CommandKind::Stop, now); }
            let mut e = self.event("command_ack"); e.id = Some(command.id); e.accepted = Some(accepted);
            e.reason = reason;
            if matches!(command.kind, CommandKind::Stop) { e.accepted = Some(true); e.reason = None; }
            e.duplicate = true; self.push(e); return;
        }
        let result = if command.id.is_empty() || command.id.len() > 64 {
            if matches!(command.kind, CommandKind::Stop) { let _ = self.apply(CommandKind::Stop, now); }
            Err("invalid_command_id")
        } else { self.apply(command.kind, now) };
        let accepted = result.is_ok();
        let reason = result.err();
        let mut e = self.event("command_ack"); e.id = Some(command.id.clone()); e.accepted = Some(accepted); e.reason = reason;
        self.push(e);
        if self.commands.len() == 32 { self.commands.pop_front(); }
        self.commands.push_back((command.id, accepted, reason));
    }
    fn apply(&mut self, kind: CommandKind, now: u64) -> Result<(), &'static str> {
        match kind {
            CommandKind::Stop => {
                self.channel = None; self.batch_pending = false;
                self.transition(State::Stopped, now);
            }
            CommandKind::Reset => {
                if self.state.active() { return Err("busy"); }
                self.fault = None; self.target_g = 0.0; self.targets = [0.0; 3]; self.delivered_g = [0.0; 3];
                self.channel = None; self.batch_pending = false;
                self.transition(State::Idle, now);
            }
            CommandKind::Tare => {
                if self.state.active() || self.fault.is_some() { return Err("busy_or_fault"); }
                if !self.valid_scale() { return Err("scale_unavailable"); }
                self.batch_pending = false; self.channel = None;
                self.begin_tare(now);
            }
            CommandKind::Start { total_g, ratios } => {
                if self.state.active() || self.fault.is_some() { return Err("busy_or_fault"); }
                if !total_g.is_finite() || !(1.0..=875.0).contains(&total_g) { return Err("invalid_total_g"); }
                if ratios.iter().any(|r| !r.is_finite() || *r < 0.0 || *r > 100.0)
                    || (ratios.iter().sum::<f32>() - 100.0).abs() > 0.01 { return Err("invalid_ratios"); }
                if !self.valid_scale() { return Err("scale_unavailable"); }
                // The physical load cell measures the vessel and all existing liquid too.
                if self.sensors.gross_weight_g + total_g > MAX_GROSS_G - CAPACITY_RESERVE_G { return Err("scale_capacity"); }
                for (i, ratio) in ratios.iter().enumerate() {
                    let needed = total_g * ratio / 100.0;
                    if needed > 0.0 && (!self.sensors.levels_pct[i].is_finite() || self.sensors.levels_pct[i] < 2.0
                        || needed + 5.0 > self.sensors.levels_pct[i] * TANK_CAPACITY_G / 100.0) { return Err("source_low"); }
                }
                self.target_g = total_g;
                self.targets = ratios.map(|r| total_g * r / 100.0);
                self.delivered_g = [0.0; 3]; self.fault = None; self.channel = None;
                self.batch_since = now; self.batch_pending = true;
                self.transition(State::Stirring, now);
            }
        }
        Ok(())
    }
    fn valid_scale(&self) -> bool { self.sensors.scale_valid && self.sensors.gross_weight_g.is_finite() && self.sensors.gross_weight_g > -10.0 }
    fn begin_tare(&mut self, now: u64) {
        self.transition(State::Taring, now);
        self.tare_stable_since = now; self.stable_g = self.sensors.gross_weight_g;
    }
    fn begin_channel(&mut self, from: usize, now: u64) {
        if let Some(i) = (from..3).find(|&i| self.targets[i] > 0.0) {
            self.channel = Some(i); self.channel_start_g = self.weight_g(); self.flow_g = self.weight_g();
            self.flow_since = now; self.dose_since = now; self.transition(State::Dosing, now);
        } else {
            self.channel = None; self.transition(State::Mixing, now);
        }
    }
    pub fn tick(&mut self, now: u64, sensors: Sensors) {
        self.sensors = sensors;
        self.outputs = Outputs::default();
        if self.state.active() {
            if !self.valid_scale() { self.fail("scale_unavailable", now); return; }
            if self.sensors.gross_weight_g > MAX_GROSS_G { self.fail("scale_overload", now); return; }
            if now.saturating_sub(self.batch_since) > 360_000 && self.batch_pending { self.fail("batch_timeout", now); return; }
            if self.sensors.levels_pct.iter().any(|v| !v.is_finite() || *v < 0.0 || *v > 101.0) {
                self.fail("level_sensor_invalid", now); return;
            }
        }
        match self.state {
            State::Stirring => {
                if now.saturating_sub(self.state_since) >= START_STIR_MS { self.begin_tare(now); }
                else { for i in 0..3 { self.outputs.stirrers[i] = self.targets[i] > 0.0; } }
            }
            State::Taring => {
                if (sensors.gross_weight_g - self.stable_g).abs() > 0.2 {
                    self.stable_g = sensors.gross_weight_g; self.tare_stable_since = now;
                }
                if now.saturating_sub(self.state_since) > 10_000 { self.fail("scale_unstable", now); return; }
                if now.saturating_sub(self.tare_stable_since) >= TARE_SETTLE_MS {
                    self.tare_g = sensors.gross_weight_g;
                    if self.batch_pending { self.begin_channel(0, now); }
                    else { self.transition(State::Idle, now); }
                }
            }
            State::Dosing => {
                let i = self.channel.unwrap();
                let amount = (self.weight_g() - self.channel_start_g).max(0.0);
                self.delivered_g[i] = amount;
                let remaining = self.targets[i] - amount;
                if remaining <= DOSE_TOLERANCE_G {
                    self.transition(State::Settling, now);
                } else if self.sensors.levels_pct[i] < 1.0 {
                    self.fail("source_empty", now);
                } else if self.weight_g() + 2.0 < self.flow_g {
                    self.fail("weight_dropped", now);
                } else if now.saturating_sub(self.dose_since) > DOSE_TIMEOUT_MS {
                    self.fail("dose_timeout", now);
                } else {
                    if self.weight_g() - self.flow_g >= 0.3 { self.flow_g = self.weight_g(); self.flow_since = now; }
                    if now.saturating_sub(self.flow_since) >= NO_FLOW_MS { self.fail("no_flow", now); return; }
                    // Relay-safe coarse/fine dosing. A 40 ms pulse supplies about 0.8 g
                    // at the simulator's 20 g/s nominal flow; wait for 100 ms scale conversion.
                    let on = remaining > 4.0 || now.saturating_sub(self.dose_since) % 350 < 40;
                    self.outputs.pumps[i] = if on { 1.0 } else { 0.0 };
                    self.outputs.stirrers[i] = true;
                }
            }
            State::Settling => {
                let i = self.channel.unwrap();
                self.delivered_g[i] = (self.weight_g() - self.channel_start_g).max(0.0);
                if now.saturating_sub(self.state_since) >= CHANNEL_SETTLE_MS {
                    if self.delivered_g[i] > self.targets[i] + 3.0 { self.fail("dose_overshoot", now); }
                    else { self.begin_channel(i + 1, now); }
                }
            }
            State::Mixing => {
                if now.saturating_sub(self.state_since) >= FINAL_MIX_MS {
                    self.batch_pending = false; self.transition(State::Complete, now);
                    let mut e = self.event("batch_complete"); e.delivered_g = Some(self.delivered_g); self.push(e);
                } else { self.outputs.stirrers[3] = true; }
            }
            _ => {}
        }
    }
}

#[cfg(test)]
mod tests {
    extern crate std;
    use super::*;
    use alloc::string::ToString;
    fn sensor(g: f32) -> Sensors { Sensors { gross_weight_g: g, levels_pct: [100.0; 3], scale_valid: true } }
    fn command(id: &str, kind: CommandKind) -> Command { Command { id: id.to_string(), session_id: None, expires_uptime_ms: None, kind } }
    fn start(c: &mut Controller) { c.tick(0, sensor(120.0)); c.command(command("start", CommandKind::Start { total_g: 100.0, ratios: [50.0, 30.0, 20.0] }), 0); }
    #[test]
    fn real_feedback_completes_sequential_batch() {
        let mut c = Controller::new(); start(&mut c);
        let mut gross = 120.0;
        let mut seen = [false; 3];
        for now in (20..60_000).step_by(20) {
            for i in 0..3 { gross += c.outputs.pumps[i] * 20.0 * 0.02; seen[i] |= c.outputs.pumps[i] > 0.0; }
            c.tick(now, sensor(gross));
            assert!(c.outputs.pumps.iter().filter(|&&x| x > 0.0).count() <= 1);
            if c.state == State::Complete { break; }
        }
        assert_eq!(c.state, State::Complete); assert!(seen.iter().all(|v| *v));
        for (actual, expected) in c.delivered_g.iter().zip([50.0,30.0,20.0]) { assert!((*actual - expected).abs() < 1.0, "{actual} expected {expected}"); }
        assert_eq!(c.outputs.pumps, [0.0; 3]); assert_eq!(c.outputs.stirrers, [false; 4]);
    }
    #[test]
    fn no_flow_and_stop_are_safe() {
        let mut c = Controller::new(); start(&mut c);
        for now in (20..12_000).step_by(20) { c.tick(now, sensor(120.0)); }
        assert_eq!(c.fault, Some("no_flow")); assert_eq!(c.outputs.pumps,[0.0;3]);
        c.command(command("stop",CommandKind::Stop),12_001); assert_eq!(c.state,State::Stopped);
        assert_eq!(c.outputs.stirrers,[false;4]);
    }
    #[test]
    fn overload_and_stale_scale_stop_pumps() {
        for (s, reason) in [(sensor(1001.0),"scale_overload"), (Sensors { scale_valid:false, ..sensor(120.0) },"scale_unavailable")] {
            let mut c=Controller::new();start(&mut c);c.tick(20,s);assert_eq!(c.fault,Some(reason));assert_eq!(c.outputs.pumps,[0.0;3]);
        }
    }
    #[test]
    fn physical_capacity_includes_cup_and_existing_liquid() {
        let mut c=Controller::new();c.tick(0,sensor(121.0));
        c.command(command("too_big",CommandKind::Start{total_g:875.0,ratios:[100.0,0.0,0.0]}),0);
        assert_eq!(c.state,State::Idle);
        assert_eq!(c.pop_event().unwrap().reason,Some("scale_capacity"));
    }
    #[test]
    fn repeated_stop_still_stops_a_later_batch_and_fault_needs_reset() {
        let mut c=Controller::new(); c.tick(0,sensor(120.0));
        c.command(command("estop",CommandKind::Stop),0);
        c.command(command("batch2",CommandKind::Start{total_g:10.0,ratios:[100.0,0.0,0.0]}),1);
        assert!(c.state.active());
        c.command(command("estop",CommandKind::Stop),2);
        assert_eq!(c.state,State::Stopped); assert_eq!(c.outputs.pumps,[0.0;3]);
        c.fail("no_flow",3); c.command(command("newstop",CommandKind::Stop),4);
        c.command(command("batch3",CommandKind::Start{total_g:10.0,ratios:[100.0,0.0,0.0]}),5);
        assert_eq!(c.state,State::Stopped); assert_eq!(c.fault,Some("no_flow"));
        c.command(command("reset",CommandKind::Reset),6); assert_eq!(c.fault,None);
    }
    #[test]
    fn duplicate_start_never_restarts_batch() {
        let mut c=Controller::new(); start(&mut c); c.tick(3000,sensor(120.0));
        c.command(command("start",CommandKind::Start{total_g:100.0,ratios:[50.0,30.0,20.0]}),3000);
        assert_ne!(c.state,State::Stirring);
    }
}
