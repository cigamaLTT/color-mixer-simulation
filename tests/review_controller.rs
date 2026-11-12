use mixer_core::{mqtt::Packet, Command, CommandKind, Controller, Device, Sensors, State};

fn command(id: &str, kind: CommandKind) -> Command {
    Command {
        id: id.into(),
        session_id: None,
        expires_uptime_ms: None,
        kind,
    }
}

fn sensors(gross: f32, delivered: [f32; 3]) -> Sensors {
    Sensors {
        gross_weight_g: gross,
        levels_pct: delivered.map(|g| 100.0 - g / 10.0),
        scale_valid: true,
    }
}

fn begin(total_g: f32, ratios: [f32; 3]) -> Controller {
    let mut controller = Controller::new();
    controller.tick(0, sensors(120.0, [0.0; 3]));
    controller.command(
        command("start", CommandKind::Start { total_g, ratios }),
        0,
    );
    assert_eq!(controller.state, State::Stirring);
    controller
}

#[test]
fn delayed_hx711_samples_complete_small_and_capacity_limit_batches() {
    // The physical plant changes between conversions. The controller receives
    // only the last complete 10 SPS sample, matching the custom HX711 model.
    for cadence_ms in [5_u64, 20, 50] {
        for phase_ms in [0_u64, 20, 50, 75] {
            for total_g in [1.0, 2.0, 5.0, 20.0, 100.0, 500.0, 875.0] {
                for ratios in [
                    [100.0, 0.0, 0.0],
                    [50.0, 30.0, 20.0],
                    [33.3, 33.3, 33.4],
                    [1.0, 1.0, 98.0],
                    [0.0, 0.0, 100.0],
                    [0.01, 49.99, 50.0],
                ] {
                    let mut controller = begin(total_g, ratios);
                    let mut delivered = [0.0_f32; 3];
                    let mut gross = 120.0;
                    let mut measured = gross;
                    let mut next_conversion_ms = 100 + phase_ms;
                    for now in (cadence_ms..180_000).step_by(cadence_ms as usize) {
                        for i in 0..3 {
                            let amount = controller.outputs.pumps[i]
                                * 20.0
                                * cadence_ms as f32
                                / 1000.0;
                            delivered[i] += amount;
                            gross += amount;
                        }
                        if now >= next_conversion_ms {
                            measured = gross;
                            next_conversion_ms = now + 100;
                        }
                        controller.tick(now, sensors(measured, delivered));
                        assert!(
                            controller.outputs.pumps.iter().filter(|&&p| p > 0.0).count() <= 1,
                            "multiple pumps active at {now} ms"
                        );
                        assert!(gross <= 1000.0, "gross scale capacity exceeded");
                        if !controller.state.active() {
                            break;
                        }
                    }
                    assert_eq!(
                        controller.state,
                        State::Complete,
                        "cadence={cadence_ms} phase={phase_ms} total={total_g} ratios={ratios:?} fault={:?}",
                        controller.fault,
                    );
                    for i in 0..3 {
                        let target = total_g * ratios[i] / 100.0;
                        assert!(
                            (delivered[i] - target).abs() <= 1.0,
                            "channel={i} cadence={cadence_ms} phase={phase_ms} total={total_g} target={target} actual={}",
                            delivered[i],
                        );
                    }
                    assert_eq!(controller.outputs.pumps, [0.0; 3]);
                    assert_eq!(controller.outputs.stirrers, [false; 4]);
                }
            }
        }
    }
}

fn dosing_controller() -> Controller {
    let mut controller = begin(100.0, [100.0, 0.0, 0.0]);
    for now in (20..=3040).step_by(20) {
        controller.tick(now, sensors(120.0, [0.0; 3]));
    }
    assert_eq!(controller.state, State::Dosing);
    assert!(controller.outputs.pumps[0] > 0.0);
    controller
}

#[test]
fn stop_and_failed_scale_clear_every_output_during_dosing() {
    let mut stopped = dosing_controller();
    stopped.command(command("stop", CommandKind::Stop), 3041);
    assert_eq!(stopped.state, State::Stopped);
    assert_eq!(stopped.outputs.pumps, [0.0; 3]);
    assert_eq!(stopped.outputs.stirrers, [false; 4]);

    let mut disconnected = dosing_controller();
    disconnected.tick(
        3041,
        Sensors { scale_valid: false, ..sensors(120.0, [0.0; 3]) },
    );
    assert_eq!(disconnected.state, State::Fault);
    assert_eq!(disconnected.fault, Some("scale_unavailable"));
    assert_eq!(disconnected.outputs.pumps, [0.0; 3]);
    assert_eq!(disconnected.outputs.stirrers, [false; 4]);
}

#[test]
fn gross_capacity_check_accounts_for_existing_paint_after_tare() {
    let mut controller = Controller::new();
    controller.tick(0, sensors(400.0, [0.0; 3]));
    controller.command(command("tare", CommandKind::Tare), 0);
    controller.tick(1000, sensors(400.0, [0.0; 3]));
    assert_eq!(controller.weight_g(), 0.0);
    controller.command(
        command("too-heavy", CommandKind::Start { total_g: 600.0, ratios: [100.0, 0.0, 0.0] }),
        1001,
    );
    assert_eq!(controller.state, State::Idle);
    assert_eq!(controller.outputs.pumps, [0.0; 3]);
    let mut rejected = false;
    while let Some(event) = controller.pop_event() {
        rejected |= event.id.as_deref() == Some("too-heavy")
            && event.accepted == Some(false)
            && event.reason == Some("scale_capacity");
    }
    assert!(rejected);
}

#[test]
fn lost_connection_latches_fault_across_mqtt_reconnect() {
    let mut device = Device::new("mixer-01".into(), "review-boot".into());
    device.controller = dosing_controller();
    device.frame(Packet::ConnAck { code: 0 }, 3041);
    device.connection_lost(3042);
    assert_eq!(device.controller.fault, Some("network_lost"));
    assert_eq!(device.controller.outputs.pumps, [0.0; 3]);
    assert_eq!(device.controller.outputs.stirrers, [false; 4]);

    device.frame(Packet::ConnAck { code: 0 }, 3043);
    device.tick(3044, sensors(120.0, [0.0; 3]));
    device.command_payload(
        br#"{"id":"new-start","session_id":"review-boot","expires_uptime_ms":8000,"type":"start","total_g":100,"ratios":[100,0,0]}"#,
        false,
        3045,
    );
    assert_eq!(device.controller.state, State::Fault);
    assert_eq!(device.controller.fault, Some("network_lost"));
    assert_eq!(device.controller.outputs.pumps, [0.0; 3]);
    assert_eq!(device.controller.outputs.stirrers, [false; 4]);
}
