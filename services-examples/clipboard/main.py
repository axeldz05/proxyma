#!/usr/bin/env python3
"""clipboard.sync — bidi NDJSON clipboard lab (last-value store per session)."""

from __future__ import annotations

import json
import sys
import time

# session_id -> {mime, data_b64}
STORE: dict[str, dict] = {}


def main() -> None:
    default_session = "default"
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            print(json.dumps({"type": "error", "error": "invalid json"}), flush=True)
            continue

        sid = str(msg.get("session_id") or default_session)
        op = str(msg.get("op") or msg.get("type") or "get").lower()

        if op in ("set", "put"):
            mime = str(msg.get("mime") or "text/plain")
            data_b64 = msg.get("data_b64")
            if data_b64 is None and "text" in msg:
                import base64

                data_b64 = base64.b64encode(str(msg["text"]).encode()).decode("ascii")
                mime = "text/plain"
            STORE[sid] = {"mime": mime, "data_b64": data_b64 or "", "ts": time.time()}
            print(
                json.dumps(
                    {
                        "type": "ok",
                        "op": "set",
                        "session_id": sid,
                        "mime": mime,
                        "ts": STORE[sid]["ts"],
                    }
                ),
                flush=True,
            )
            continue

        if op in ("get", "pull"):
            cur = STORE.get(sid)
            if not cur:
                print(
                    json.dumps(
                        {
                            "type": "empty",
                            "op": "get",
                            "session_id": sid,
                        }
                    ),
                    flush=True,
                )
            else:
                print(
                    json.dumps(
                        {
                            "type": "ok",
                            "op": "get",
                            "session_id": sid,
                            **cur,
                        }
                    ),
                    flush=True,
                )
            continue

        print(
            json.dumps(
                {
                    "type": "error",
                    "error": f"unknown op {op!r}; use set|get",
                    "session_id": sid,
                }
            ),
            flush=True,
        )


if __name__ == "__main__":
    main()
