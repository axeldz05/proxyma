#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_service_subscribe"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Service subscribe notify filter...${NC}"

install_e2e_case_trap "case-18-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1/scripts" \
    "$E2E_DATA_DIR/node-1/schemas" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-2/schemas" \
    "$E2E_DATA_DIR/node-3/scripts" \
    "$E2E_DATA_DIR/node-3/schemas"

cat >"$E2E_DATA_DIR/node-1/scripts/source.py" <<'PY'
import json
print(json.dumps({"text": "source-text"}))
PY
cat >"$E2E_DATA_DIR/node-2/scripts/vision.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({"status": "ok", "svc": "vision.ocr", "input": payload.get("input")}))
PY
cat >"$E2E_DATA_DIR/node-3/scripts/audio.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({"status": "ok", "svc": "audio.transcribe", "input": payload.get("input")}))
PY

cat >"$E2E_DATA_DIR/node-1/schemas/source.json" <<'JSON'
{
  "name": "subscription.source",
  "type": "script",
  "parameters": {},
  "outputs": {
    "text": {"type": "string"}
  }
}
JSON
cat >"$E2E_DATA_DIR/node-2/schemas/vision.json" <<'JSON'
{
  "name": "vision.ocr",
  "type": "script",
  "description": "Matching vision contract",
  "parameters": {
    "input": {"type": "int", "required": false}
  },
  "outputs": {
    "svc": {"type": "string"}
  }
}
JSON
cat >"$E2E_DATA_DIR/node-3/schemas/audio.json" <<'JSON'
{
  "name": "audio.transcribe",
  "type": "script",
  "description": "Non-matching audio contract",
  "parameters": {
    "input": {"type": "int", "required": false}
  },
  "outputs": {
    "svc": {"type": "string"}
  }
}
JSON

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083
run_node node-1 service add \
    --name subscription.source \
    --type script \
    --exec "python3 /app/data/scripts/source.py" \
    --schema-file /app/data/schemas/source.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

# mTLS health only proves listeners are ready. Service notifications require
# the authenticated senders to be registered in the receiver's public topology.
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-1 node-2
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-1 node-3

exec_node node-1 ./proxyma service subscribe \
    --name "vision.*" --storage /app/data >/dev/null

exec_node node-2 ./proxyma service add \
    --name vision.ocr \
    --type script \
    --exec "python3 /app/data/scripts/vision.py" \
    --schema-file /app/data/schemas/vision.json \
    --storage /app/data >/dev/null
exec_node node-3 ./proxyma service add \
    --name audio.transcribe \
    --type script \
    --exec "python3 /app/data/scripts/audio.py" \
    --schema-file /app/data/schemas/audio.json \
    --storage /app/data >/dev/null

# Send duplicate public mTLS notifications and wait for their acknowledgements.
# This removes the asynchronous gossip timing window using public endpoints.
call_peer_api node-2 node-1 POST 8081 services/notify \
    -H "Content-Type: application/json" \
    -d '{"action":"add","node_id":"node-2","schema":{"name":"vision.ocr","type":"script","parameters":{"input":{"type":"int","required":false}},"outputs":{"svc":{"type":"string"}}}}' \
    >/dev/null
call_peer_api node-3 node-1 POST 8081 services/notify \
    -H "Content-Type: application/json" \
    -d '{"action":"add","node_id":"node-3","schema":{"name":"audio.transcribe","type":"script","parameters":{"input":{"type":"int","required":false}},"outputs":{"svc":{"type":"string"}}}}' \
    >/dev/null

# Offline pipeline validation must enforce the subscribed vision schema while
# the non-subscribed audio schema remains unavailable to validation.
VISION_MISMATCH='{"id":"vision-offline-validation","version":1,"steps":[{"id":"source","service":"subscription.source"},{"id":"target","service":"vision.ocr"}],"connections":[{"from_step":"source","from_port":"text","to_step":"target","to_port":"input"}]}'
AUDIO_MISMATCH='{"id":"audio-offline-validation","version":1,"steps":[{"id":"source","service":"subscription.source"},{"id":"target","service":"audio.transcribe"}],"connections":[{"from_step":"source","from_port":"text","to_step":"target","to_port":"input"}]}'

set +e
VISION_VALIDATION=$(exec_node node-1 ./proxyma service add_pipeline \
    --id vision-offline-validation \
    --schema "$VISION_MISMATCH" \
    --storage /app/data 2>&1)
VISION_EXIT=$?
set -e
if [ "$VISION_EXIT" -eq 0 ]; then
    fail_assertion "Subscribed vision schema was unavailable to offline validation" "$VISION_VALIDATION"
fi
assert_contains "$VISION_VALIDATION" "type mismatch" \
    "Offline validation did not enforce the subscribed vision schema"

AUDIO_VALIDATION=$(exec_node node-1 ./proxyma service add_pipeline \
    --id audio-offline-validation \
    --schema "$AUDIO_MISMATCH" \
    --storage /app/data)
assert_contains "$AUDIO_VALIDATION" "Pipeline added successfully" \
    "Non-subscribed audio schema unexpectedly affected offline validation"
exec_node node-1 ./proxyma service remove_pipeline \
    --id audio-offline-validation --storage /app/data >/dev/null

VISION_RUN=$(exec_node node-1 ./proxyma service run \
    --name vision.ocr --payload '{"input":7}' --storage /app/data)
assert_contains "$VISION_RUN" '"status": "completed"' \
    "Matching vision service was not runnable"
assert_contains "$VISION_RUN" '"svc": "vision.ocr"' \
    "Matching vision service returned the wrong provider output"

e2e_compose stop node-3 >/dev/null
DISCOVER=$(exec_node node-1 ./proxyma service discover --storage /app/data)
assert_contains "$DISCOVER" vision.ocr \
    "Matching vision service disappeared while its provider was available"
assert_not_contains "$DISCOVER" audio.transcribe \
    "Offline non-matching audio service remained discoverable"

set +e
AUDIO_RUN=$(exec_node node-1 ./proxyma service run \
    --name audio.transcribe --payload '{"input":7}' --storage /app/data 2>&1)
AUDIO_EXIT=$?
set -e
if [ "$AUDIO_EXIT" -eq 0 ]; then
    fail_assertion "Non-matching audio service remained runnable without its provider" "$AUDIO_RUN"
fi
assert_contains "$AUDIO_RUN" "no nodes available" \
    "Non-matching audio run failed for an unexpected reason"

echo -e "${GREEN}✅ Case 18 (matching available, non-matching absent) passed${NC}"
