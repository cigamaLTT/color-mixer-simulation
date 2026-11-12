#!/usr/bin/env python3
"""Record device MQTT traffic as JSON lines."""

import argparse
import json
from pathlib import Path
import socket
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tests"))
from network import MQTT


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=9001)
    parser.add_argument("--device-id", default="mixer-01")
    parser.add_argument("--tcp", action="store_true", help="use MQTT TCP instead of MQTT WebSocket")
    parser.add_argument("--duration", type=float, default=0, help="real seconds; 0 records until interrupted")
    args = parser.parse_args()
    started = time.monotonic()
    def active():
        return not args.duration or time.monotonic() - started < args.duration

    try:
        while active():
            try:
                with MQTT(args.host, args.port, f"observer-{time.time_ns():x}", websocket=not args.tcp) as client:
                    client.subscribe(f"mixer/{args.device_id}/#")
                    print(json.dumps({"type": "observer_ready", "transport": "MQTT TCP" if args.tcp else "MQTT over WebSocket",
                                      "device_id": args.device_id, "observed_at": time.time()}), flush=True)
                    last_ping = time.monotonic()
                    while active():
                        if time.monotonic() - last_ping > 10:
                            client.socket.sendall(b"\xc0\x00")
                            last_ping = time.monotonic()
                        try:
                            message = client.receive(timeout=1)
                        except (TimeoutError, socket.timeout):
                            continue
                        try:
                            message["payload"] = json.loads(message["payload"])
                        except (json.JSONDecodeError, UnicodeDecodeError):
                            message["payload"] = message["payload"].decode(errors="replace")
                        message["observed_at"] = time.time()
                        print(json.dumps(message, separators=(",", ":")), flush=True)
            except (OSError, EOFError, ConnectionError) as error:
                print(f"MQTT observer reconnecting: {error}", file=sys.stderr, flush=True)
                time.sleep(1)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
