#!/usr/bin/env python3
"""media.watermark — draw text watermark on image."""

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

from PIL import Image, ImageDraw, ImageFont  # noqa: E402


def main() -> None:
    try:
        payload = json.load(sys.stdin)
        input_path = payload.get("input_path") or payload.get("image_path")
        if not input_path or not os.path.isfile(input_path):
            raise ValueError(f"input_path not found: {input_path!r}")
        text = str(payload.get("text") or "proxyma")
        output_path = payload.get("output_path") or f"/tmp/proxyma_wm_{Path(input_path).name}"

        img = Image.open(input_path).convert("RGBA")
        overlay = Image.new("RGBA", img.size, (0, 0, 0, 0))
        draw = ImageDraw.Draw(overlay)
        font = ImageFont.load_default()
        # bottom-right-ish
        x = max(8, img.width - 8 - 8 * len(text))
        y = max(8, img.height - 24)
        draw.text((x, y), text, fill=(255, 255, 255, 180), font=font)
        out = Image.alpha_composite(img, overlay).convert("RGB")
        Path(output_path).parent.mkdir(parents=True, exist_ok=True)
        out.save(output_path)
        print(
            json.dumps(
                {
                    "status": "ok",
                    "output_path": output_path,
                    "message": f"watermarked with {text!r}",
                }
            )
        )
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
