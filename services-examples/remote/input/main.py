#!/usr/bin/env python3
"""remote.input — bidi NDJSON echo lab (stdin events → stdout acks)."""

from __future__ import annotations

import json
import sys
import time


def main() -> None:
    session_id = ""
    # First line may be a bootstrap payload from the engine; accept either.
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            print(json.dumps({"type": "error", "error": "invalid json"}), flush=True)
            continue

        if not session_id:
            session_id = str(msg.get("session_id") or "")

        sid = str(msg.get("session_id") or session_id)
        ev_type = msg.get("type") or msg.get("event") or "unknown"
        ack = {
            "type": "ack",
            "session_id": sid,
            "event": ev_type,
            "ts": time.time(),
            "echo": {
                "x": msg.get("x"),
                "y": msg.get("y"),
                "key": msg.get("key"),
                "buttons": msg.get("buttons"),
            },
            "applied": False,
            "message": "lab echo — OS inject not enabled",
        }
        print(json.dumps(ack), flush=True)


if __name__ == "__main__":
    main()
