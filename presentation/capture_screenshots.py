#!/usr/bin/env python3
"""Capture one real native-controller batch from the running dashboard."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import urllib.request

from playwright.sync_api import expect, sync_playwright


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8088")
    parser.add_argument("--broker", default="127.0.0.1:1883")
    parser.add_argument("--device-id", default="presentation-demo")
    parser.add_argument("--chromium", default="/usr/bin/chromium")
    args = parser.parse_args()
    here = Path(__file__).resolve().parent
    repo = here.parent
    assets = here / "assets"
    evidence = here / "evidence"
    assets.mkdir(exist_ok=True)
    evidence.mkdir(exist_ok=True)
    base = args.base_url.rstrip("/")

    def snapshot() -> dict:
        with urllib.request.urlopen(base + "/api/state", timeout=5) as response:
            state = json.load(response)
        return next(d for d in state["devices"] if d["device_id"] == args.device_id)

    with urllib.request.urlopen(base + "/api/state", timeout=5) as response:
        before = json.load(response)
    if any(d["device_id"] == args.device_id and d.get("online") for d in before["devices"]):
        raise RuntimeError("Choose a device ID that has no active controller")

    frames: list[dict] = []
    errors: list[str] = []
    captures: list[dict] = []
    command = [str(repo / "firmware/target/release/virtual-device"),
               "--broker", args.broker, "--device-id", args.device_id,
               "--speed", "1", "--verbose"]
    with (evidence / "native.log").open("w") as log:
        process = subprocess.Popen(command, cwd=repo, stdout=log, stderr=subprocess.STDOUT)
        try:
            with sync_playwright() as pw:
                browser = pw.chromium.launch(executable_path=args.chromium, headless=True,
                                             args=["--no-sandbox"])
                page = browser.new_page(viewport={"width": 1440, "height": 1100},
                                        device_scale_factor=2, reduced_motion="reduce")
                page.on("pageerror", lambda error: errors.append(str(error)))

                def track(ws) -> None:
                    def read(frame) -> None:
                        try:
                            message = json.loads(frame)
                            if message.get("device_id") == args.device_id:
                                frames.append(message)
                        except (ValueError, TypeError):
                            pass
                    ws.on("framereceived", read)
                page.on("websocket", track)
                page.goto(base, wait_until="networkidle")
                page.wait_for_function("id => [...document.querySelector('#device-select').options].some(o => o.value === id)", arg=args.device_id)
                page.select_option("#device-select", args.device_id)
                expect(page.locator("#live-label")).to_have_text("NATIVE · TRỰC TIẾP")
                expect(page.locator("#start-button")).to_be_enabled()
                page.fill("#total-input", "100")
                for i, ratio in enumerate((50, 30, 20)):
                    page.fill(f"#ratio-{i}", str(ratio))
                expect(page.locator("#ratio-total")).to_have_text("100%")

                def shot(name: str, selector: str) -> None:
                    page.locator(selector).screenshot(path=str(assets / name), animations="disabled")

                def record(stage: str) -> None:
                    captures.append({"stage": stage,
                                     "captured_at": datetime.now(timezone.utc).isoformat(),
                                     "device": snapshot()})

                shot("01-setup.png", ".recipe-card")
                record("setup")
                page.click("#start-button")
                expect(page.locator("#metric-state")).to_have_text("Đang định lượng", timeout=15000)
                page.locator('[data-component="pump-0"]').click(force=True)
                shot("02-dosing.png", "#machine-svg")
                shot("02-dosing-state.png", ".metric:nth-child(4)")
                record("dosing")

                expect(page.locator("#metric-state")).to_have_text("Đang pha trộn", timeout=20000)
                page.locator('[data-component="scale"]').click(force=True)
                # Crop the actual SVG to the receiving cup and its active stirrer
                # This changes only the screenshot bounds, never the page or data
                svg = page.locator("#machine-svg").bounding_box()
                scale = svg["width"] / 920
                page.screenshot(path=str(assets / "03-mixing.png"), animations="disabled",
                                clip={"x": svg["x"] + 700 * scale,
                                      "y": svg["y"] + 220 * scale,
                                      "width": 170 * scale, "height": 245 * scale})
                shot("03-mixing-state.png", ".metric:nth-child(4)")
                record("mixing")

                expect(page.locator("#metric-state")).to_have_text("Hoàn tất", timeout=10000)
                expect(page.locator("#event-log")).to_contain_text("Hoàn tất")
                shot("04-complete-chart.png", ".chart-wrap")
                shot("04-complete-state.png", ".metric:nth-child(4)")
                page.locator("#event-log li").filter(has_text="Hoàn tất").first.screenshot(
                    path=str(assets / "04-complete-log.png"), animations="disabled")
                record("complete")
                assert snapshot()["telemetry"]["state"] == "complete"
                assert any(f.get("type") == "event" and f.get("data", {}).get("type") == "command_ack"
                           and f.get("data", {}).get("accepted") for f in frames)
                assert not errors, errors
                browser.close()
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
    report = {"source": "Native Rust controller, software plant, real MQTT and dashboard WebSocket",
              "base_url": base, "device_id": args.device_id,
              "recipe": {"total_g": 100, "ratios": [50, 30, 20]},
              "speed": 1, "captures": captures, "browser_errors": errors,
              "events": [f for f in frames if f.get("type") in ("event", "command_ack")]}
    (evidence / "capture.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print("Captured setup, dosing, mixing and completion from a real native session")


if __name__ == "__main__":
    main()
