#!/usr/bin/env python3
"""HTTP NDJSON upstream for sensor.telemetry (server_stream)."""

from __future__ import annotations

import json
import math
import os
import random
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


DEFAULT_PORT = int(os.environ.get("PROXYMA_SENSOR_PORT", "19101"))
DEFAULT_TICKS = int(os.environ.get("PROXYMA_SENSOR_TICKS", "0"))  # 0 = until client disconnect


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt: str, *args) -> None:
        return

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw.decode() or "{}")
        except json.JSONDecodeError:
            payload = {}

        ticks = int(payload.get("ticks") or DEFAULT_TICKS or 0)
        interval_ms = float(payload.get("interval_ms") or 200)
        sensor_id = str(payload.get("sensor_id") or "lab-1")

        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson")
        self.end_headers()

        n = 0
        t0 = time.time()
        while ticks == 0 or n < ticks:
            n += 1
            elapsed = time.time() - t0
            chunk = {
                "n": n,
                "sensor_id": sensor_id,
                "ts": time.time(),
                "cpu_pct": round(35 + 10 * math.sin(elapsed), 2),
                "temp_c": round(42 + 3 * math.sin(elapsed / 2), 2),
                "lat": 40.4 + 0.001 * math.sin(elapsed / 5),
                "lon": -3.7 + 0.001 * math.cos(elapsed / 5),
                "noise": round(random.random(), 4),
            }
            line = (json.dumps(chunk) + "\n").encode()
            try:
                self.wfile.write(line)
                self.wfile.flush()
            except BrokenPipeError:
                return
            if ticks > 0 and n >= ticks:
                return
            time.sleep(max(interval_ms, 1) / 1000.0)


def main() -> None:
    port = DEFAULT_PORT
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"sensor.telemetry upstream on http://127.0.0.1:{port}/", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
