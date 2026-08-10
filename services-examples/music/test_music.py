#!/usr/bin/env python3
"""Unit tests for music.resolve + music.convert (no Proxyma daemon)."""

from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest
import wave
from pathlib import Path

ROOT = Path(__file__).resolve().parent


def _load(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


resolve_mod = _load("resolve_logic", ROOT / "resolve" / "logic.py")
convert_mod = _load("convert_logic", ROOT / "convert" / "logic.py")


def write_silent_wav(path: Path, seconds: float = 0.05, rate: int = 8000) -> None:
    nframes = int(rate * seconds)
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "w") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(rate)
        wf.writeframes(b"\x00\x00" * nframes)


class ResolveTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.lib = Path(self.tmp.name)
        track = self.lib / "song-a"
        write_silent_wav(track / "song-a.wav")
        (track / "song-a.flac").write_bytes(b"fLaCfake")  # extension-only for resolve
        (track / "song-a.mp3").write_bytes(b"ID3fake")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_auto_picks_flac(self) -> None:
        out = resolve_mod.resolve_track(self.lib, "song-a", "auto")
        self.assertEqual(out["source_format"], "flac")
        self.assertEqual(out["requested_format"], "auto")
        self.assertFalse(out["needs_convert"])
        self.assertTrue(out["audio_path"].endswith(".flac"))

    def test_mp3_present_picks_mp3(self) -> None:
        out = resolve_mod.resolve_track(self.lib, "song-a", "mp3")
        self.assertEqual(out["source_format"], "mp3")
        self.assertFalse(out["needs_convert"])

    def test_mp3_missing_picks_best_source(self) -> None:
        track = self.lib / "song-b"
        write_silent_wav(track / "song-b.wav")
        (track / "song-b.flac").write_bytes(b"fLaCfake")
        out = resolve_mod.resolve_track(self.lib, "song-b", "mp3")
        self.assertEqual(out["source_format"], "flac")
        self.assertEqual(out["requested_format"], "mp3")
        self.assertTrue(out["needs_convert"])


class ConvertTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.wav = Path(self.tmp.name) / "t.wav"
        write_silent_wav(self.wav)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_passthrough_auto(self) -> None:
        out = convert_mod.convert_audio(str(self.wav), "wav", "auto")
        self.assertFalse(out["converted"])
        self.assertEqual(out["audio_path"], str(self.wav.resolve()))

    def test_passthrough_same_format(self) -> None:
        out = convert_mod.convert_audio(str(self.wav), "wav", "wav")
        self.assertFalse(out["converted"])

    def test_convert_wav_to_mp3(self) -> None:
        if os.system("ffmpeg -version >/dev/null 2>&1") != 0:
            self.skipTest("ffmpeg not installed")
        out_dir = Path(self.tmp.name) / "out"
        out = convert_mod.convert_audio(
            str(self.wav), "wav", "mp3", out_dir=str(out_dir)
        )
        self.assertTrue(out["converted"])
        self.assertTrue(out["audio_path"].endswith(".mp3"))
        self.assertTrue(Path(out["audio_path"]).is_file())


if __name__ == "__main__":
    unittest.main()
