#!/usr/bin/env python3
"""media.resize — unary image resize with Pillow."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

script_dir = Path(__file__).resolve().parent
services_root = script_dir.parent.parent
venv_python = services_root / ".venv" / "bin" / "python"
if venv_python.exists() and Path(sys.executable) != venv_python and "PROXYMA_REEXEC" not in os.environ:
    os.environ["PROXYMA_REEXEC"] = "1"
    os.execv(str(venv_python), [str(venv_python), *sys.argv])

from PIL import Image  # noqa: E402


def main() -> None:
    try:
        payload = json.load(sys.stdin)
        input_path = payload.get("input_path") or payload.get("image_path")
        if not input_path or not os.path.isfile(input_path):
            raise ValueError(f"input_path not found: {input_path!r}")
        width = int(payload.get("width") or 256)
        height = int(payload.get("height") or width)
        output_path = payload.get("output_path") or f"/tmp/proxyma_resize_{Path(input_path).name}"

        img = Image.open(input_path)
        img = img.convert("RGB") if img.mode not in ("RGB", "RGBA") else img
        img = img.resize((width, height))
        Path(output_path).parent.mkdir(parents=True, exist_ok=True)
        img.save(output_path)
        print(
            json.dumps(
                {
                    "status": "ok",
                    "output_path": output_path,
                    "width": width,
                    "height": height,
                    "message": f"resized to {width}x{height}",
                }
            )
        )
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
