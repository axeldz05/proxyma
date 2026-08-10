#!/usr/bin/env bash
# Start HTTP upstreams required by server_stream examples (sensor + music.stream).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PROXYMA_SENSOR_PORT="${PROXYMA_SENSOR_PORT:-19101}"
export PROXYMA_MUSIC_STREAM_PORT="${PROXYMA_MUSIC_STREAM_PORT:-19102}"

mkdir -p "$ROOT/.run"
PIDFILE="$ROOT/.run/upstreams.pids"
: >"$PIDFILE"

python3 "$ROOT/sensor/telemetry_server.py" >>"$ROOT/.run/sensor.log" 2>&1 &
echo $! >>"$PIDFILE"
python3 "$ROOT/music/stream/stream_server.py" >>"$ROOT/.run/music_stream.log" 2>&1 &
echo $! >>"$PIDFILE"

echo "Upstreams started (pids in $PIDFILE)"
echo "  sensor.telemetry → http://127.0.0.1:${PROXYMA_SENSOR_PORT}/"
echo "  music.stream     → http://127.0.0.1:${PROXYMA_MUSIC_STREAM_PORT}/"
echo "Stop with: kill \$(cat $PIDFILE)"
