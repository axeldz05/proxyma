#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_screen_stream"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Screen stream (fake frames)...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2"

bootstrap_node node-1 8081
bootstrap_node node-2 8082

# Capturer: in-process fake JPEG frames (no Android device)
run_node node-2 service add \
    --name "screen-share" \
    --storage "/app/data" \
    --type "screen" \
    --exec "fake" \
    --desc "Fake screen capturer" \
    --param "frames?:int"

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
$COMPOSE_CMD up -d node-2
sleep 2

# 45 frames × 50ms ≈ 2.25s of stream (assert ≥2s wall + gaps)
echo "Streaming screen frames from node-1 (viewer) → node-2 (capturer)..."
START_TS=$(date +%s)
STREAM_OUT=$(call_api node-1 POST 8081 "services/stream?service=screen-share" \
    -H "Content-Type: application/json" \
    -d '{"frames":45}')
END_TS=$(date +%s)
ELAPSED=$((END_TS - START_TS))
echo "stream elapsed=${ELAPSED}s"
echo "stream sample: $(echo "$STREAM_OUT" | head -c 200)..."

FRAME_COUNT=$(echo "$STREAM_OUT" | grep -c 'frame_b64' || true)
if [ "$FRAME_COUNT" -lt 10 ]; then
    echo -e "${RED}❌ Expected ≥10 screen frames, got $FRAME_COUNT${NC}"
    exit 1
fi
if [ "$ELAPSED" -lt 2 ]; then
    echo -e "${RED}❌ Expected stream duration ≥2s, got ${ELAPSED}s${NC}"
    exit 1
fi
if echo "$STREAM_OUT" | grep -qi 'not implemented\|501\|not supported'; then
    echo -e "${RED}❌ Stream fell through to not-implemented/501${NC}"
    exit 1
fi

# Soft gap check: first vs last n should span ≥2s of paced frames
FIRST_N=$(echo "$STREAM_OUT" | head -1 | sed -n 's/.*"n":\([0-9.]*\).*/\1/p')
LAST_N=$(echo "$STREAM_OUT" | grep 'frame_b64' | tail -1 | sed -n 's/.*"n":\([0-9.]*\).*/\1/p')
echo "frame n range: $FIRST_N → $LAST_N (count=$FRAME_COUNT)"

# JPEG magic on first frame_b64
echo "$STREAM_OUT" | python3 -c '
import sys, json, base64
lines = [ln for ln in sys.stdin if "frame_b64" in ln]
assert lines, "no frames"
obj = json.loads(lines[0])
raw = base64.b64decode(obj["frame_b64"])
assert raw[:3] == b"\xff\xd8\xff", f"bad jpeg magic: {raw[:3]!r}"
print(f"jpeg magic ok ({len(raw)} bytes)")
'

echo -e "${GREEN}✅ Case 14 (screen stream fake frames) passed — $FRAME_COUNT frames in ${ELAPSED}s${NC}"
exit 0
