"""Socket integration tests for the backend and shared Rust controller.

Run with `make test-integration`.
"""

import json
import math
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request

from network import MQTT, WebSocket, until_json


ROOT = Path(__file__).resolve().parents[1]


def available_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class Integration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.http_port, cls.mqtt_port, cls.mqtt_ws_port = [available_port() for unused in range(3)]
        cls.base = f"http://127.0.0.1:{cls.http_port}"
        cls.websocket = f"ws://127.0.0.1:{cls.http_port}/ws"
        cls.temp = tempfile.TemporaryDirectory(prefix="mixer-e2e-")
        cls.log_path = Path(cls.temp.name) / "backend.log"
        cls.log = cls.log_path.open("w+")
        env = os.environ.copy()
        for name in ("MQTT_URL", "MQTT_USERNAME", "MQTT_PASSWORD"):
            env.pop(name, None)
        env.update(HTTP_ADDR=f"127.0.0.1:{cls.http_port}",
                   MQTT_ADDR=f"127.0.0.1:{cls.mqtt_port}",
                   MQTT_WS_ADDR=f"127.0.0.1:{cls.mqtt_ws_port}",
                   WEB_DIR=str(ROOT / "iot/web"),
                   DEVICE_ID="integration-fixture", STALE_AFTER="1200ms", OFFLINE_AFTER="3s")
        binary = Path(os.environ.get("IOT_BINARY", str(ROOT / ".build/mixer-iot")))
        if not binary.is_file():
            raise RuntimeError("Run make build-host before the integration tests")
        cls.backend = subprocess.Popen([str(binary)], cwd=ROOT, env=env,
                                       stdout=cls.log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            if cls.backend.poll() is not None:
                cls.log.flush()
                raise RuntimeError("backend exited: " + cls.log_path.read_text())
            try:
                with urllib.request.urlopen(cls.base + "/healthz", timeout=0.5) as response:
                    if json.load(response).get("broker_connected"):
                        return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.05)
        cls.backend.terminate()
        cls.backend.wait(timeout=5)
        raise RuntimeError("backend/broker not ready: " + cls.log_path.read_text())

    @classmethod
    def tearDownClass(cls):
        cls.backend.terminate()
        try:
            cls.backend.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.backend.kill()
            cls.backend.wait(timeout=5)
        cls.log.close()
        cls.temp.cleanup()

    def request(self, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.base + path, data=data,
                                         headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=3) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            with response:
                return response.code, json.load(response)

    def state(self, device_id):
        unused, snapshot = self.request("/api/state")
        return next((device for device in snapshot["devices"] if device["device_id"] == device_id), None)

    def wait_state(self, device_id, predicate, timeout=5):
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            last = self.state(device_id)
            if last and predicate(last):
                return last
            time.sleep(0.04)
        self.fail(f"device {device_id!r} did not reach expected state: {last!r}")

    def test_01_empty_dashboard_has_no_manufactured_measurements(self):
        status, snapshot = self.request("/api/state")
        self.assertEqual(status, 200)
        self.assertTrue(snapshot["broker"]["connected"])
        self.assertTrue(all(device.get("telemetry") is None for device in snapshot["devices"]))
        with urllib.request.urlopen(self.base + "/", timeout=3) as response:
            html = response.read().decode()
            self.assertEqual(response.status, 200)
            self.assertIn("<html", html.lower())

    def test_02_tcp_mqtt_ws_http_and_app_ws_contract(self):
        fixture = json.loads((ROOT / "tests/fixtures/telemetry.json").read_text())
        device_id = fixture["device_id"]
        telemetry_topic = f"mixer/{device_id}/telemetry"
        command_topic = f"mixer/{device_id}/command"
        with MQTT("127.0.0.1", self.mqtt_port, "e2e-tcp") as device, \
             MQTT("127.0.0.1", self.mqtt_ws_port, "e2e-mqtt-ws", websocket=True) as mqtt_ws, \
             WebSocket(self.websocket) as app:
            self.assertEqual(app.receive_json()["type"], "state")
            mqtt_ws.subscribe(telemetry_topic)
            mqtt_ws.subscribe(command_topic)
            device.publish(telemetry_topic, fixture)
            mirrored = mqtt_ws.receive()
            self.assertEqual(mirrored["topic"], telemetry_topic)
            self.assertEqual(json.loads(mirrored["payload"]), fixture)
            received = until_json(app, lambda event: event.get("type") == "telemetry"
                                  and event.get("data", {}).get("device_id") == device_id)
            self.assertEqual(received["data"]["gross_weight_g"], 120)
            self.wait_state(device_id, lambda state: state["online"] and not state["stale"])

            status, result = self.request("/api/command", {
                "device_id": device_id,
                "command": {"id": "e2e-http-start", "type": "start", "total_g": 100, "ratios": [50, 30, 20]},
            })
            self.assertEqual(status, 202, result)
            self.assertEqual(result["status"], "published")
            forwarded = mqtt_ws.receive()
            self.assertEqual(forwarded["topic"], command_topic)
            self.assertFalse(forwarded["retain"])
            command = json.loads(forwarded["payload"])
            self.assertEqual(command["id"], "e2e-http-start")
            self.assertEqual(command["session_id"], fixture["session_id"])
            self.assertGreater(command["expires_uptime_ms"], fixture["uptime_ms"])

            device.publish(f"mixer/{device_id}/event", {
                "id": command["id"], "type": "command_ack", "accepted": True, "state": "stirring",
            })
            ack = until_json(app, lambda event: event.get("type") == "event"
                             and event.get("data", {}).get("id") == command["id"])
            self.assertTrue(ack["data"]["accepted"])

            # The second route goes app WebSocket -> backend -> MQTT WebSocket.
            app.send_json({"type": "command", "device_id": device_id,
                           "command": {"id": "e2e-ws-stop", "type": "stop"}})
            publication = until_json(app, lambda event: event.get("type") == "command_ack"
                                     and event.get("data", {}).get("id") == "e2e-ws-stop")
            self.assertEqual(publication["data"]["status"], "published")
            forwarded = mqtt_ws.receive()
            self.assertEqual(json.loads(forwarded["payload"])["type"], "stop")
            self.assertFalse(forwarded["retain"])

            # Invalid recipe must never become a command publication.
            status, result = self.request("/api/command", {
                "device_id": device_id,
                "command": {"type": "start", "total_g": 876, "ratios": [50, 30, 20]},
            })
            self.assertGreaterEqual(status, 400, result)

            # A malformed frame may not replace a valid sensor observation.
            device.publish(telemetry_topic, b'{"state":"dosing"')
            time.sleep(0.1)
            self.assertEqual(self.state(device_id)["telemetry"]["seq"], 1)

        stale = self.wait_state(device_id, lambda state: state["stale"] and state["online"], timeout=2.5)
        self.assertEqual(stale["telemetry"]["seq"], 1)
        self.wait_state(device_id, lambda state: not state["online"], timeout=4)

    def native_device(self, device_id, fault=None):
        binary = Path(os.environ.get("NATIVE_DEVICE_BINARY", str(ROOT / "firmware/target/release/virtual-device")))
        args = [str(binary), "--broker", f"127.0.0.1:{self.mqtt_port}",
                "--device-id", device_id, "--speed", "10"]
        if fault:
            args += ["--fault", fault]
        log = (Path(self.temp.name) / f"{device_id}.log").open("w+")
        process = subprocess.Popen(args, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
        self.addCleanup(log.close)
        self.addCleanup(self.stop_process, process)
        self.wait_state(device_id, lambda state: state["online"] and state.get("telemetry") is not None)
        return process

    @staticmethod
    def stop_process(process):
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)

    def test_03_real_shared_controller_completes_batch_and_stops(self):
        device_id = "native-e2e-batch"
        process = self.native_device(device_id)
        with WebSocket(self.websocket) as app, \
             MQTT("127.0.0.1", self.mqtt_ws_port, "e2e-batch-mqtt-ws", websocket=True) as mqtt_ws:
            self.assertEqual(app.receive_json()["type"], "state")
            mqtt_ws.subscribe(f"mixer/{device_id}/telemetry")
            status, result = self.request("/api/command", {
                "device_id": device_id,
                "command": {"id": "e2e-real-batch", "type": "start", "total_g": 100, "ratios": [50, 30, 20]},
            })
            self.assertEqual(status, 202, result)
            ack = until_json(app, lambda event: event.get("type") == "event"
                             and event.get("data", {}).get("id") == "e2e-real-batch", timeout=5)
            self.assertTrue(ack["data"].get("accepted"), ack)
            observed = set()
            pump_seen = [False, False, False]
            deadline = time.monotonic() + 20
            finished = None
            while time.monotonic() < deadline:
                message = mqtt_ws.receive(timeout=max(0.1, deadline - time.monotonic()))
                telemetry = json.loads(message["payload"])
                observed.add(telemetry["state"])
                self.assertTrue(telemetry["scale_valid"])
                self.assertTrue(math.isfinite(telemetry["weight_g"]))
                self.assertLessEqual(telemetry["gross_weight_g"], 1000)
                for index, output in enumerate(telemetry["pumps"]):
                    pump_seen[index] |= output > 0
                if telemetry["state"] == "complete":
                    finished = telemetry
                    break
                if telemetry["state"] == "fault":
                    self.fail(f"unexpected controller fault: {telemetry!r}")
            self.assertIsNotNone(finished, f"batch never completed, observed {observed}")
            self.assertTrue(all(pump_seen), pump_seen)
            self.assertLessEqual(abs(finished["weight_g"] - 100), 3)
            for actual, expected in zip(finished["delivered_g"], [50, 30, 20]):
                self.assertLessEqual(abs(actual - expected), 2)
            self.assertEqual(finished["pumps"], [0, 0, 0])
            self.assertFalse(any(finished["stirrers"]))

            # Reset is sent over the app WS route. It must reach the controller.
            app.send_json({"type": "command", "device_id": device_id,
                           "command": {"id": "e2e-real-reset", "type": "reset"}})
            reset = until_json(app, lambda event: event.get("type") == "event"
                               and event.get("data", {}).get("id") == "e2e-real-reset", timeout=5)
            self.assertTrue(reset["data"].get("accepted"), reset)
            self.wait_state(device_id, lambda state: state["telemetry"]["state"] == "idle")
            status, result = self.request("/api/command", {
                "device_id": device_id,
                "command": {"id": "e2e-real-start-stop", "type": "start", "total_g": 100, "ratios": [50, 30, 20]},
            })
            self.assertEqual(status, 202, result)
            self.wait_state(device_id, lambda state: state["telemetry"]["state"] not in ("idle", "complete"))
            status, result = self.request("/api/command", {
                "device_id": device_id, "command": {"id": "e2e-real-stop", "type": "stop"},
            })
            self.assertEqual(status, 202, result)
            stopped = self.wait_state(device_id, lambda state: state["telemetry"]["state"] == "stopped")
            self.assertFalse(any(stopped["telemetry"]["pumps"]))
            self.assertFalse(any(stopped["telemetry"]["stirrers"]))
            self.assertIsNone(process.poll())

    def test_04_sensor_fault_cannot_complete_batch(self):
        device_id = "native-e2e-no-flow"
        self.native_device(device_id, fault="no_flow")
        status, result = self.request("/api/command", {
            "device_id": device_id,
            "command": {"id": "e2e-no-flow", "type": "start", "total_g": 100, "ratios": [50, 30, 20]},
        })
        self.assertEqual(status, 202, result)
        fault = self.wait_state(device_id, lambda state: state["telemetry"]["state"] == "fault", timeout=12)
        self.assertIsNotNone(fault["telemetry"]["fault"])
        self.assertFalse(any(fault["telemetry"]["pumps"]))
        self.assertFalse(any(fault["telemetry"]["stirrers"]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
