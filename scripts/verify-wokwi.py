#!/usr/bin/env python3
"""Run acceptance commands against a running Wokwi ESP32.

Starts a 100 g 50/30/20 recipe. With --check-stop, first starts and stops a
separate trial, then resets the controller before the complete recipe.
"""

import argparse
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import socket
import sys
import threading
import time
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tests"))
from network import MQTT, WebSocket


def emit(kind, **data):
    print(json.dumps({"type": kind, "observed_at": datetime.now(timezone.utc).isoformat(),
                      **data}, separators=(",", ":")), flush=True)


def require(condition, detail):
    if not condition:
        raise RuntimeError(detail)


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.session = None
        self.current = None
        self.app_messages = []
        self.mqtt_messages = []
        self.mqtt_error = None
        self.lock = threading.Lock()
        self.done = threading.Event()

    def request(self, path, body=None):
        request = urllib.request.Request(
            self.args.base_url.rstrip("/") + path,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=3) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            with response:
                raise RuntimeError(f"HTTP {response.code}: {response.read().decode()}")

    def snapshot(self):
        unused, snapshot = self.request("/api/state")
        return next((device for device in snapshot["devices"]
                     if device["device_id"] == self.args.device_id), None)

    def wait_online(self):
        emit("waiting_for_actual_esp32", base_url=self.args.base_url,
             device_id=self.args.device_id, timeout_seconds=self.args.wait_online)
        deadline = time.monotonic() + self.args.wait_online
        while time.monotonic() < deadline:
            device = self.snapshot()
            telemetry = device.get("telemetry") if device else None
            if (telemetry and device["online"] and not device["stale"]
                    and telemetry["session_id"].startswith("esp32-")):
                self.session = telemetry["session_id"]
                self.current = telemetry
                require(telemetry["state"] == "idle", f"ESP32 must start idle: {telemetry}")
                require(telemetry["scale_valid"], "ESP32 scale is invalid")
                emit("actual_esp32_telemetry", device=device)
                return
            time.sleep(.2)
        raise TimeoutError("Actual ESP32 telemetry did not arrive; no native fallback was started")

    def collect_mqtt(self, client):
        last_ping = time.monotonic()
        try:
            while not self.done.is_set():
                if time.monotonic() - last_ping > 10:
                    client.socket.sendall(b"\xc0\x00")
                    last_ping = time.monotonic()
                try:
                    message = client.receive(timeout=.25)
                except (TimeoutError, socket.timeout):
                    continue
                message["payload"] = json.loads(message["payload"])
                with self.lock:
                    self.mqtt_messages.append(message)
        except Exception as error:
            if not self.done.is_set():
                self.mqtt_error = error

    def receive_app(self, timeout):
        if self.mqtt_error:
            raise RuntimeError(f"MQTT WebSocket observer failed: {self.mqtt_error}")
        self.app.settimeout(timeout)
        message = self.app.receive_json()
        self.app_messages.append(message)
        if (message["type"] == "telemetry"
                and message.get("device_id") == self.args.device_id):
            telemetry = message["data"]
            require(telemetry["session_id"] == self.session, "ESP32 restarted during acceptance")
            require(telemetry["scale_valid"] and math.isfinite(telemetry["weight_g"]),
                    f"Invalid ESP32 scale telemetry: {telemetry}")
            require(telemetry["state"] != "fault", f"ESP32 controller fault: {telemetry}")
            if self.current["state"] != telemetry["state"]:
                emit("esp32_state_over_app_ws", telemetry=telemetry)
            self.current = telemetry
        return message

    def until_app(self, predicate, timeout=None):
        deadline = time.monotonic() + (timeout or self.args.timeout)
        while time.monotonic() < deadline:
            message = self.receive_app(max(.01, deadline - time.monotonic()))
            if predicate(message):
                return message
        raise TimeoutError("Expected application WebSocket observation did not arrive")

    def wait_telemetry(self, predicate):
        return self.until_app(lambda message: message["type"] == "telemetry"
                              and message.get("device_id") == self.args.device_id
                              and predicate(message["data"]))["data"]

    def command(self, action, route="app_ws", **fields):
        identifier = f"wokwi-{action}-{time.time_ns():x}"
        envelope = {"device_id": self.args.device_id,
                    "command": {"id": identifier, "type": action, **fields}}
        if route == "http":
            status, publication = self.request("/api/command", envelope)
            require(status == 202 and publication.get("status") == "published",
                    f"HTTP command publication failed: {publication}")
        else:
            self.app.send_json({"type": "command", **envelope})
            publication = self.until_app(
                lambda message: message["type"] in ("command_ack", "error")
                and message.get("data", {}).get("id") == identifier, timeout=30)
            require(publication["type"] == "command_ack"
                    and publication["data"].get("status") == "published",
                    f"App WebSocket command publication failed: {publication}")
        emit("backend_command_publication", route=route, command_id=identifier,
             action=action, publication=publication)
        acknowledgement = self.until_app(
            lambda message: message["type"] == "event"
            and message.get("device_id") == self.args.device_id
            and message.get("data", {}).get("id") == identifier, timeout=60)
        require(acknowledgement["data"].get("accepted") is True,
                f"ESP32 rejected command: {acknowledgement}")
        emit("esp32_command_ack", route=route, message=acknowledgement)
        return identifier

    def mqtt_command(self, identifier):
        with self.lock:
            command = next((message for message in self.mqtt_messages
                            if message["topic"].endswith("/command")
                            and message["payload"].get("id") == identifier), None)
        require(command is not None, f"Command {identifier} missing from MQTT WebSocket")
        require(not command["retain"], "Command was retained")
        if command["payload"]["type"] != "stop":
            require(command["payload"].get("session_id") == self.session,
                    "Command did not target the current ESP32 session")
        emit("command_over_mqtt_ws", message=command)

    def stop_trial(self):
        start = self.command("start", total_g=100, ratios=[50, 30, 20])
        active = self.wait_telemetry(lambda telemetry: any(telemetry["pumps"]))
        emit("stop_trial_active_pump", telemetry=active)
        stop = self.command("stop", route="http")
        stopped = self.wait_telemetry(lambda telemetry: telemetry["state"] == "stopped")
        require(not any(stopped["pumps"]) and not any(stopped["stirrers"]),
                f"Outputs remain active after stop: {stopped}")
        self.mqtt_command(start)
        self.mqtt_command(stop)
        emit("verified_actual_esp32_stop", telemetry=stopped)
        self.command("reset")
        self.wait_telemetry(lambda telemetry: telemetry["state"] == "idle")

    def batch(self):
        offset = len(self.app_messages)
        starting_levels = self.current["levels_pct"]
        start = self.command("start", total_g=100, ratios=[50, 30, 20])
        complete = self.wait_telemetry(lambda telemetry: telemetry["state"] == "complete")
        require(abs(complete["weight_g"] - 100) <= 3, f"Batch mass outside ±3 g: {complete}")
        require(all(abs(actual - expected) <= 2 for actual, expected
                    in zip(complete["delivered_g"], [50, 30, 20])),
                f"Per-channel mass outside ±2 g: {complete}")
        require(not any(complete["pumps"]) and not any(complete["stirrers"]),
                f"Outputs remain active after completion: {complete}")
        app_samples = [message["data"] for message in self.app_messages[offset:]
                       if message["type"] == "telemetry"
                       and message.get("device_id") == self.args.device_id]
        require(all(any(sample["pumps"][channel] > 0 for sample in app_samples)
                    for channel in range(3)), "Not all three pumps observed over app WebSocket")
        states = {sample["state"] for sample in app_samples}
        require({"stirring", "taring", "dosing", "settling", "mixing", "complete"} <= states,
                f"Missing batch phase observations: {sorted(states)}")
        require(all(after < before for before, after in zip(starting_levels, complete["levels_pct"])),
                "A source level failed to decrease during the batch")
        deadline = time.monotonic() + 5
        mirrored = None
        while time.monotonic() < deadline:
            if self.mqtt_error:
                raise RuntimeError(f"MQTT WebSocket observer failed: {self.mqtt_error}")
            with self.lock:
                mqtt_samples = [message["payload"] for message in self.mqtt_messages
                                if message["topic"].endswith("/telemetry")
                                and message["payload"].get("session_id") == self.session]
            mirrored = next((sample for sample in mqtt_samples
                             if sample["seq"] == complete["seq"]), None)
            if mirrored:
                break
            time.sleep(.05)
        require(mirrored == complete, "Completion not mirrored exactly over MQTT WebSocket")
        self.mqtt_command(start)
        snapshot = self.snapshot()
        require(snapshot and snapshot["online"] and not snapshot["stale"]
                and snapshot["telemetry"]["session_id"] == self.session
                and snapshot["telemetry"]["state"] == "complete",
                f"HTTP snapshot did not match completion: {snapshot}")
        emit("verified_actual_wokwi_batch", command_id=start,
             states=sorted(states), starting_levels_pct=starting_levels,
             app_ws_telemetry=complete, mqtt_ws_telemetry=mirrored, http_snapshot=snapshot)

    def run(self):
        self.wait_online()
        websocket_url = self.args.base_url.replace("http://", "ws://", 1).rstrip("/") + "/ws"
        with MQTT(self.args.mqtt_host, self.args.mqtt_ws_port,
                  f"wokwi-acceptance-{time.time_ns():x}", websocket=True) as client, \
                WebSocket(websocket_url) as app:
            self.app = app
            require(app.receive_json()["type"] == "state", "Missing initial application snapshot")
            client.subscribe(f"mixer/{self.args.device_id}/#")
            collector = threading.Thread(target=self.collect_mqtt, args=(client,), daemon=True)
            collector.start()
            try:
                if self.args.check_stop:
                    self.stop_trial()
                self.batch()
            except Exception:
                try:
                    unused, result = self.request("/api/command", {
                        "device_id": self.args.device_id,
                        "command": {"id": f"wokwi-verifier-stop-{time.time_ns():x}", "type": "stop"}})
                    emit("cleanup_stop_publication", publication=result)
                except Exception as error:
                    emit("cleanup_stop_failed", error=repr(error))
                raise
            finally:
                self.done.set()
                collector.join(timeout=2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8088")
    parser.add_argument("--mqtt-host", default="127.0.0.1")
    parser.add_argument("--mqtt-ws-port", type=int, default=9001)
    parser.add_argument("--device-id", default="mixer-01")
    parser.add_argument("--wait-online", type=float, default=180, help="seconds to wait for actual ESP32")
    parser.add_argument("--timeout", type=float, default=600, help="seconds per controller phase")
    parser.add_argument("--check-stop", action="store_true", help="test HTTP stop during dosing before final batch")
    args = parser.parse_args()
    try:
        Acceptance(args).run()
    except Exception as error:
        emit("verification_failed", error=repr(error))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
