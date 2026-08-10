#!/usr/bin/env python3
"""music.convert — idempotent ffmpeg convert step."""

from __future__ import annotations

import json
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from logic import convert_audio  # noqa: E402


def main() -> None:
    try:
        payload = json.load(sys.stdin)
        audio_path = payload.get("audio_path") or ""
        source_format = payload.get("source_format") or ""
        requested = (
            payload.get("requested_format")
            or payload.get("format")
            or "auto"
        )
        out_dir = payload.get("out_dir")
        out = convert_audio(
            audio_path,
            source_format,
            requested,
            out_dir=out_dir,
        )
        print(json.dumps(out))
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
