#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_peer_restart_persistence"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Sponsor restart persistence...${NC}"

install_e2e_case_trap "case-22-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-2/schemas"

cat >"$E2E_DATA_DIR/node-2/scripts/restart_echo.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({
    "status": "ok",
    "provider": "node-2",
    "message": payload.get("message"),
    "contract": "peer-topology-persisted"
}))
PY
cat >"$E2E_DATA_DIR/node-2/schemas/restart_echo.json" <<'JSON'
{
  "name": "restart.echo",
  "type": "script",
  "description": "Sponsor restart persistence contract",
  "parameters": {
    "message": {"type": "string", "required": false}
  },
  "outputs": {
    "provider": {"type": "string"},
    "message": {"type": "string"},
    "contract": {"type": "string"}
  }
}
JSON

bootstrap_node node-1 8081
bootstrap_node node-2 8082
run_node node-2 service add \
    --name restart.echo \
    --type script \
    --exec "python3 /app/data/scripts/restart_echo.py" \
    --schema-file /app/data/schemas/restart_echo.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

wait_for_output "${E2E_PEER_TIMEOUT:-45}" node-2 \
    exec_node node-1 ./proxyma peers list --storage /app/data >/dev/null

BEFORE_RESTART=$(exec_node node-1 ./proxyma service run \
    --name restart.echo \
    --payload '{"message":"before-restart"}' \
    --storage /app/data)
assert_contains "$BEFORE_RESTART" '"status": "completed"' \
    "Sponsor could not run the peer service before restart"
assert_contains "$BEFORE_RESTART" '"message": "before-restart"' \
    "Peer service returned unexpected pre-restart output"

# Compose restart preserves the same node-1 bind-mounted data volume.
restart_node node-1 8081

TELEMETRY=$(call_api node-1 GET 8081 telemetry)
assert_contains "$TELEMETRY" '"node_id":"node-1"' \
    "Restarted sponsor did not retain its public node identity"

PEERS_AFTER_RESTART=$(exec_node node-1 ./proxyma peers list --storage /app/data)
assert_contains "$PEERS_AFTER_RESTART" node-2 \
    "Restarted sponsor lost persisted peer topology"

AFTER_RESTART=$(exec_node node-1 ./proxyma service run \
    --name restart.echo \
    --payload '{"message":"after-restart"}' \
    --storage /app/data)
assert_contains "$AFTER_RESTART" '"status": "completed"' \
    "Restarted sponsor could not run the persisted peer route"
assert_contains "$AFTER_RESTART" '"message": "after-restart"' \
    "Peer service returned unexpected post-restart output"
assert_contains "$AFTER_RESTART" '"contract": "peer-topology-persisted"' \
    "Peer service operation did not survive sponsor restart"

echo -e "${GREEN}✅ Case 22 (sponsor topology and service persistence) passed${NC}"
