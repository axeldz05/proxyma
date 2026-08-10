#!/usr/bin/env python3
"""shell.attach — lab bidi shell (safe echo + allowlisted builtins only)."""

from __future__ import annotations

import json
import os
import platform
import sys
import time


def handle_line(line: str) -> str:
    cmd = line.strip()
    if not cmd:
        return ""
    if cmd in ("help", "?"):
        return "lab builtins: help, ping, uname, pwd, echo <text>, exit\n"
    if cmd == "ping":
        return "pong\n"
    if cmd == "uname":
        return f"{platform.system()} {platform.release()}\n"
    if cmd == "pwd":
        return os.getcwd() + "\n"
    if cmd.startswith("echo "):
        return cmd[5:] + "\n"
    if cmd in ("exit", "quit"):
        return "__EXIT__"
    return f"lab: refused (no real PTY). unknown: {cmd!r}\n"


def main() -> None:
    print(
        json.dumps(
            {
                "type": "stdout",
                "data": "proxyma shell.attach lab — type help\n",
                "ts": time.time(),
            }
        ),
        flush=True,
    )
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            # raw line treated as stdin
            msg = {"type": "stdin", "data": line}

        if msg.get("type") in ("stdin", "input", None) or "data" in msg:
            data = str(msg.get("data") or "")
            out = handle_line(data)
            if out == "__EXIT__":
                print(
                    json.dumps({"type": "exit", "code": 0, "ts": time.time()}),
                    flush=True,
                )
                return
            print(
                json.dumps({"type": "stdout", "data": out, "ts": time.time()}),
                flush=True,
            )
            continue

        print(
            json.dumps(
                {
                    "type": "error",
                    "error": "expected {type:stdin, data:...}",
                    "ts": time.time(),
                }
            ),
            flush=True,
        )


if __name__ == "__main__":
    main()
