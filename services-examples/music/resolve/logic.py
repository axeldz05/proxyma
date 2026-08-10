"""Deterministic track format resolution (no I/O side effects beyond listing)."""

from __future__ import annotations

from pathlib import Path

# Highest quality first.
FORMAT_PRIORITY = ("flac", "wav", "alac", "aiff", "mp3", "ogg", "m4a")
EXT_TO_FORMAT = {
    ".flac": "flac",
    ".wav": "wav",
    ".alac": "alac",
    ".aiff": "aiff",
    ".aif": "aiff",
    ".mp3": "mp3",
    ".ogg": "ogg",
    ".m4a": "m4a",
}


def normalize_format(fmt: str | None) -> str:
    if not fmt or fmt.strip() == "" or fmt.strip().lower() == "auto":
        return "auto"
    return fmt.strip().lower().lstrip(".")


def list_track_files(library_root: Path, track_id: str) -> dict[str, Path]:
    """Return map format -> path for files under library_root/track_id/."""
    track_dir = library_root / track_id
    found: dict[str, Path] = {}
    if not track_dir.is_dir():
        return found
    for path in sorted(track_dir.iterdir()):
        if not path.is_file():
            continue
        fmt = EXT_TO_FORMAT.get(path.suffix.lower())
        if fmt and fmt not in found:
            found[fmt] = path
    return found


def pick_highest(available: dict[str, Path]) -> tuple[Path, str] | None:
    if not available:
        return None
    for fmt in FORMAT_PRIORITY:
        if fmt in available:
            return available[fmt], fmt
    fmt, path = next(iter(available.items()))
    return path, fmt


def resolve_track(
    library_root: Path, track_id: str, requested_format: str = "auto"
) -> dict:
    """Pick source file for streaming/convert.

    - auto: highest quality available; convert will no-op.
    - specific format present: that file; convert no-op.
    - specific format missing: highest quality source so convert can produce target.
    """
    if not track_id:
        raise ValueError("track_id is required")
    available = list_track_files(library_root, track_id)
    if not available:
        raise ValueError(f"no audio files for track_id={track_id!r} under {library_root}")
    req = normalize_format(requested_format)

    if req != "auto" and req in available:
        path = available[req]
        source_format = req
    else:
        picked = pick_highest(available)
        assert picked is not None
        path, source_format = picked

    return {
        "status": "ok",
        "audio_path": str(path.resolve()),
        "source_format": source_format,
        "requested_format": req,
        "track_id": track_id,
        "available_formats": sorted(available.keys()),
        "needs_convert": req != "auto" and source_format != req,
    }
