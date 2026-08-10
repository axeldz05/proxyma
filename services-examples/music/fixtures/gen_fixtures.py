#!/usr/bin/env python3
"""Generate tiny lab fixtures under music/fixtures/library/."""

from __future__ import annotations

import subprocess
import wave
from pathlib import Path

ROOT = Path(__file__).resolve().parent
LIBRARY = ROOT / "library"


def write_silent_wav(path: Path, seconds: float = 0.25, rate: int = 16000) -> None:
    nframes = int(rate * seconds)
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "w") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(rate)
        wf.writeframes(b"\x00\x00" * nframes)


def main() -> None:
    # demo-hi: flac + wav (no mp3) → convert path when format=mp3
    hi = LIBRARY / "demo-hi"
    wav_hi = hi / "demo-hi.wav"
    write_silent_wav(wav_hi)
    flac_hi = hi / "demo-hi.flac"
    subprocess.run(
        ["ffmpeg", "-y", "-i", str(wav_hi), "-codec:a", "flac", str(flac_hi)],
        check=True,
        capture_output=True,
    )

    # demo-mp3: already has mp3 (+ wav) → pass-through when format=mp3
    mp3_track = LIBRARY / "demo-mp3"
    wav_m = mp3_track / "demo-mp3.wav"
    write_silent_wav(wav_m)
    mp3_path = mp3_track / "demo-mp3.mp3"
    subprocess.run(
        [
            "ffmpeg",
            "-y",
            "-i",
            str(wav_m),
            "-codec:a",
            "libmp3lame",
            "-q:a",
            "4",
            str(mp3_path),
        ],
        check=True,
        capture_output=True,
    )

    print(f"fixtures ready under {LIBRARY}")


if __name__ == "__main__":
    main()
