#!/usr/bin/env python3
"""Run one real ESP-IDF/Wokwi acceptance batch against an isolated backend."""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import threading
import time
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
LOGS = ROOT / ".logs"
BRIDGE = "http://127.0.0.1:18999/wokwi-vscode."
DEVICE_ID = "mixer-idf-trial"


def bridge(action: str) -> None:
    request = urllib.request.Request(BRIDGE + action, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:
        print(f"{action} {response.status}", flush=True)


def wait_http(url: str, process: subprocess.Popen[bytes]) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("isolated backend exited during startup")
        try:
            with urllib.request.urlopen(url, timeout=1):
                return
        except OSError:
            time.sleep(0.1)
    raise TimeoutError("isolated backend readiness timeout")


def connect_uart() -> socket.socket:
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            connection = socket.create_connection(("127.0.0.1", 4000), timeout=2)
            connection.settimeout(0.5)
            return connection
        except OSError:
            time.sleep(0.25)
    raise TimeoutError("Wokwi RFC2217 UART did not open after simulator startup")


def main() -> int:
    LOGS.mkdir(exist_ok=True)
    original_toml = (ROOT / "wokwi.toml").read_bytes()
    backend = verifier = None
    serial = None
    simulator_started = False
    stopped = threading.Event()
    fatal: list[str] = []

    def read_uart() -> None:
        assert serial is not None
        transcript = bytearray()
        try:
            with (LOGS / "idf-cycle-uart.log").open("wb") as output:
                while not stopped.is_set():
                    try:
                        data = serial.recv(65536)
                    except socket.timeout:
                        continue
                    if not data:
                        raise RuntimeError("UART connection closed while simulator was running")
                    transcript.extend(data)
                    output.write(data)
                    output.flush()
                    text = data.decode("utf-8", "replace")
                    print(text, end="", flush=True)
                    bad = re.search(
                        rb"MIXER_PANIC|MIXER_FATAL|Guru Meditation|assert failed|abort\(\)|"
                        rb"MQTT_WORKER_ERROR|state=Fault|fault=Some",
                        transcript,
                    )
                    if bad:
                        fatal.append(transcript[max(0, bad.start() - 120):bad.end() + 300].decode("utf-8", "replace"))
                        return
        except Exception as error:  # surfaced by the main monitoring loop
            if not stopped.is_set():
                fatal.append(repr(error))

    try:
        environment = os.environ.copy()
        environment.update({
            "HTTP_ADDR": "127.0.0.1:8089",
            "MQTT_ADDR": "127.0.0.1:1884",
            "MQTT_WS_ADDR": "127.0.0.1:9002",
            "DEVICE_ID": DEVICE_ID,
            "STALE_AFTER": "60s",
            "OFFLINE_AFTER": "180s",
            "WEB_DIR": str(ROOT / "iot/web"),
        })
        with (LOGS / "idf-cycle-backend.log").open("wb") as output:
            backend = subprocess.Popen([str(ROOT / ".build/mixer-iot")], cwd=ROOT,
                                       env=environment, stdout=output, stderr=subprocess.STDOUT)
        wait_http("http://127.0.0.1:8089/api/state", backend)

        bridge("stopSimulation")
        idf_toml = original_toml.replace(b"firmware/esp32-mixer.elf", b"firmware/esp32-mixer-idf.elf")
        if b"firmware/esp32-mixer-idf.elf" not in idf_toml:
            raise RuntimeError("wokwi.toml does not contain the ESP-IDF firmware path")
        (ROOT / "wokwi.toml").write_bytes(idf_toml)
        bridge("start")
        simulator_started = True
        # RFC2217 is intentionally opened only after start: stopping a Wokwi
        # simulation closes its UART listener.
        serial = connect_uart()
        threading.Thread(target=read_uart, daemon=True).start()

        with (LOGS / "idf-cycle-events.ndjson").open("wb") as output:
            verifier = subprocess.Popen([
                sys.executable, "scripts/verify-wokwi.py", "--base-url", "http://127.0.0.1:8089",
                "--mqtt-ws-port", "9002", "--device-id", DEVICE_ID,
                "--wait-online", "360", "--timeout", "1200",
            ], cwd=ROOT, stdout=output, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 1800
        while time.monotonic() < deadline:
            if fatal:
                raise RuntimeError("UART error: " + fatal[0])
            if backend.poll() is not None:
                raise RuntimeError("isolated backend exited; see .logs/idf-cycle-backend.log")
            status = verifier.poll()
            if status is not None:
                if status:
                    detail = (LOGS / "idf-cycle-events.ndjson").read_text(errors="replace")[-3500:]
                    raise RuntimeError("acceptance verifier failed:\n" + detail)
                (LOGS / "idf-cycle-result.json").write_text(json.dumps({"passed": True}, indent=2) + "\n")
                print("IDF_CYCLE_SUCCESS", flush=True)
                return 0
            time.sleep(0.2)
        raise TimeoutError("single Wokwi acceptance cycle exceeded 1800 seconds")
    except Exception as error:
        (LOGS / "idf-cycle-result.json").write_text(json.dumps({"passed": False, "first_error": str(error)}, indent=2) + "\n")
        print(f"FIRST_ERROR {error!r}", flush=True)
        if simulator_started and backend and backend.poll() is None:
            stop = {"device_id": DEVICE_ID, "command": {"id": f"idf-fail-safe-{time.time_ns():x}", "type": "stop"}}
            try:
                request = urllib.request.Request("http://127.0.0.1:8089/api/command",
                    data=json.dumps(stop).encode(), headers={"Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=3) as response:
                    print(f"FAIL_SAFE_STOP {response.status}", flush=True)
            except Exception as cleanup_error:
                print(f"FAIL_SAFE_STOP_FAILED {cleanup_error!r}", flush=True)
        return 1
    finally:
        stopped.set()
        if verifier and verifier.poll() is None:
            verifier.terminate()
        try:
            if simulator_started:
                bridge("stopSimulation")
        except Exception as error:
            print(f"STOP_SIMULATION_FAILED {error!r}", flush=True)
        if serial:
            serial.close()
        if backend and backend.poll() is None:
            backend.terminate()
            try:
                backend.wait(timeout=5)
            except subprocess.TimeoutExpired:
                backend.kill()
        (ROOT / "wokwi.toml").write_bytes(original_toml)


if __name__ == "__main__":
    raise SystemExit(main())
