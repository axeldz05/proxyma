"""Idempotent audio convert step (ffmpeg when formats differ)."""

from __future__ import annotations

import os
import subprocess
import tempfile
from pathlib import Path


def normalize_format(fmt: str | None) -> str:
    if not fmt or fmt.strip() == "" or fmt.strip().lower() == "auto":
        return "auto"
    return fmt.strip().lower().lstrip(".")


def should_passthrough(source_format: str, requested_format: str) -> bool:
    src = normalize_format(source_format)
    req = normalize_format(requested_format)
    return req == "auto" or src == req


def convert_audio(
    audio_path: str,
    source_format: str,
    requested_format: str,
    *,
    ffmpeg_bin: str = "ffmpeg",
    out_dir: str | None = None,
) -> dict:
    """Pass-through or ffmpeg convert. Always returns typed outputs."""
    path = Path(audio_path)
    if not path.is_file():
        raise ValueError(f"audio_path not found: {audio_path}")

    src = normalize_format(source_format) or path.suffix.lstrip(".").lower()
    req = normalize_format(requested_format)

    if should_passthrough(src, req):
        return {
            "status": "ok",
            "audio_path": str(path.resolve()),
            "source_format": src,
            "requested_format": req,
            "converted": False,
            "message": "pass-through",
        }

    work = Path(out_dir) if out_dir else Path(tempfile.mkdtemp(prefix="proxyma_music_"))
    work.mkdir(parents=True, exist_ok=True)
    out_path = work / f"{path.stem}.{req}"

    cmd = [
        ffmpeg_bin,
        "-y",
        "-i",
        str(path),
        "-vn",
    ]
    if req == "mp3":
        cmd += ["-codec:a", "libmp3lame", "-q:a", "2"]
    elif req == "wav":
        cmd += ["-codec:a", "pcm_s16le"]
    elif req == "flac":
        cmd += ["-codec:a", "flac"]
    elif req == "ogg":
        cmd += ["-codec:a", "libvorbis", "-q:a", "5"]
    else:
        raise ValueError(f"unsupported target format: {req}")

    cmd.append(str(out_path))

    try:
        proc = subprocess.run(
            cmd,
            check=False,
            capture_output=True,
            text=True,
            timeout=int(os.environ.get("PROXYMA_FFMPEG_TIMEOUT", "120")),
        )
    except FileNotFoundError as exc:
        raise ValueError(f"ffmpeg not found ({ffmpeg_bin})") from exc
    except subprocess.TimeoutExpired as exc:
        raise ValueError("ffmpeg timed out") from exc

    if proc.returncode != 0 or not out_path.is_file():
        err = (proc.stderr or proc.stdout or "").strip()[-500:]
        raise ValueError(f"ffmpeg failed converting to {req}: {err}")

    return {
        "status": "ok",
        "audio_path": str(out_path.resolve()),
        "source_format": src,
        "requested_format": req,
        "converted": True,
        "message": f"converted {src} -> {req}",
    }
