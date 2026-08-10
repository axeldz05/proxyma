#!/usr/bin/env python3
"""music.resolve — pick library file by track_id + format."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from logic import resolve_track  # noqa: E402

DEFAULT_LIBRARY = SCRIPT_DIR.parent / "fixtures" / "library"


def main() -> None:
    try:
        payload = json.load(sys.stdin)
        track_id = payload.get("track_id") or ""
        requested = payload.get("format") or payload.get("requested_format") or "auto"
        library = Path(
            payload.get("library_path")
            or os.environ.get("PROXYMA_MUSIC_LIBRARY")
            or DEFAULT_LIBRARY
        )
        out = resolve_track(library, track_id, requested)
        print(json.dumps(out))
    except Exception as exc:  # noqa: BLE001 — surface as script error JSON
        print(json.dumps({"error": str(exc)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
