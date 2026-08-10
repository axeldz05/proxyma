# Proxyma service examples

Lab services and pipelines that exercise streaming, VFS staging, and unary DAGs.

## Register

After `scripts/bootstrap_dev.sh` (or manually):

```bash
pm service add --name "$PWD/services-examples/sensor/telemetry_service.json"
# …same for each *_service.json below
pm service add_pipeline --id music-prepare-pipeline --schema-file "$PWD/services-examples/music/music_prepare_pipeline.json"
```

`server_stream` examples need HTTP upstreams:

```bash
./services-examples/start_upstreams.sh
```

## Catalog

| Service / pipeline | Type | Role |
|--------------------|------|------|
| `sensor.telemetry` | `server_stream` | NDJSON CPU/temp/GPS ticks |
| `music.resolve` | `script` | Pick library file (`auto` = best quality) |
| `music.convert` | `script` | Idempotent ffmpeg (no-op if `auto` or same format) |
| `music-prepare-pipeline` | pipeline | `resolve → convert` |
| `music.stream` | `server_stream` | Chunk prepared audio as `{seq,codec,b64}` |
| `remote.screen` | `screen` | Fake JPEG frames (pair with input) |
| `remote.input` | `bidi_stream` | Input echo lab (`session_id`) |
| `media.resize` / `media.watermark` | `script` | Thumbnail building blocks |
| `media-thumbnail-pipeline` | pipeline | `resize → watermark` |
| `clipboard.sync` | `bidi_stream` | set/get clipboard JSON |
| `shell.attach` | `bidi_stream` | Allowlisted lab shell (no real PTY) |

## Music: prepare then stream

Pipelines are unary; continuous stream is a separate call.

```bash
# 1) Prepare (auto → flac for demo-hi; mp3 → convert when missing)
pm service run --name music-prepare-pipeline \
  --param track_id=demo-hi --param format=mp3

# 2) Stream the resulting audio_path from the pipeline output
pm service run --name music.stream --param audio_path=/path/from/step_convert
```

Fixtures live under `music/fixtures/library/` (`demo-hi` has flac+wav only; `demo-mp3` already has mp3). Regenerate:

```bash
python3 services-examples/music/fixtures/gen_fixtures.py
python3 services-examples/music/test_music.py
```

## Remote session (lab)

```bash
# Terminal A — screen
pm service run --name remote.screen --param session_id=demo --param frames=30

# Terminal B — input (send NDJSON lines with session_id, type, x, y, key)
pm service run --name remote.input --param session_id=demo
```

`remote.screen` uses in-process `fake` frames (no upstream). Input only echoes until a real OS inject is wired.

## Sensor smoke

```bash
./services-examples/start_upstreams.sh
pm service run --name sensor.telemetry --param ticks=5 --param interval_ms=50
```

## Notes

- Prefer direct/QUIC paths for media streams (relay body limit is small).
- `shell.attach` refuses arbitrary commands on purpose.
- Convert offload: register `music.convert` on a peer and run prepare with `--strategy cheapest`.
