#!/usr/bin/env python3
"""HTTP NDJSON upstream for music.stream — chunked base64 audio frames."""

from __future__ import annotations

import base64
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

DEFAULT_PORT = int(os.environ.get("PROXYMA_MUSIC_STREAM_PORT", "19102"))
CHUNK_SIZE = int(os.environ.get("PROXYMA_MUSIC_CHUNK", "4096"))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt: str, *args) -> None:
        return

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw.decode() or "{}")
        except json.JSONDecodeError:
            self.send_error(400, "invalid json")
            return

        audio_path = payload.get("audio_path") or ""
        path = Path(audio_path)
        if not path.is_file():
            self.send_response(400)
            self.send_header("Content-Type", "application/x-ndjson")
            self.end_headers()
            self.wfile.write(
                (json.dumps({"error": f"audio_path not found: {audio_path}"}) + "\n").encode()
            )
            return

        codec = path.suffix.lstrip(".").lower() or "bin"
        chunk_size = int(payload.get("chunk_size") or CHUNK_SIZE)

        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson")
        self.end_headers()

        seq = 0
        with path.open("rb") as fh:
            while True:
                data = fh.read(chunk_size)
                if not data:
                    break
                seq += 1
                chunk = {
                    "seq": seq,
                    "codec": codec,
                    "b64": base64.b64encode(data).decode("ascii"),
                    "bytes": len(data),
                    "path": str(path),
                }
                try:
                    self.wfile.write((json.dumps(chunk) + "\n").encode())
                    self.wfile.flush()
                except BrokenPipeError:
                    return

        try:
            self.wfile.write(
                (
                    json.dumps(
                        {
                            "seq": seq,
                            "codec": codec,
                            "eof": True,
                            "message": "stream complete",
                        }
                    )
                    + "\n"
                ).encode()
            )
            self.wfile.flush()
        except BrokenPipeError:
            return


def main() -> None:
    port = DEFAULT_PORT
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"music.stream upstream on http://127.0.0.1:{port}/", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
