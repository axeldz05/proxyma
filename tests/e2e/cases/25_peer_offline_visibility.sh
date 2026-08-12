#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_peer_offline_visibility"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Peer offline visibility and recovery...${NC}"

install_e2e_case_trap "case-25-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-2/schemas"

cat >"$E2E_DATA_DIR/node-2/scripts/offline_echo.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({
    "provider": "node-2",
    "echo": payload.get("message"),
    "contract": "peer-recovered",
}))
PY
cat >"$E2E_DATA_DIR/node-2/schemas/offline_echo.json" <<'JSON'
{
  "name": "peer.offline.echo",
  "type": "script",
  "description": "Peer offline visibility contract",
  "parameters": {
    "message": {"type": "string", "required": true}
  },
  "outputs": {
    "provider": {"type": "string"},
    "echo": {"type": "string"},
    "contract": {"type": "string"}
  }
}
JSON

bootstrap_node node-1 8081
bootstrap_node node-2 8082
run_node node-2 service add \
    --name peer.offline.echo \
    --type script \
    --exec "python3 /app/data/scripts/offline_echo.py" \
    --schema-file /app/data/schemas/offline_echo.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" peer.offline.echo \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

INITIAL_RUN=$(exec_node node-1 ./proxyma service run \
    --name peer.offline.echo \
    --payload '{"message":"before-stop"}' \
    --storage /app/data)
printf '%s\n' "$INITIAL_RUN" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
outputs = response.get("outputs", {})
expected = {
    "status": "completed",
    "provider": "node-2",
    "echo": "before-stop",
}
actual = {
    "status": response.get("status"),
    "provider": outputs.get("provider"),
    "echo": outputs.get("echo"),
}
if actual != expected:
    raise SystemExit(f"unexpected pre-stop response: {actual!r}")
'

ONLINE_PEERS=$(exec_node node-1 ./proxyma peers list --storage /app/data)
ONLINE_STATUS=$(printf '%s\n' "$ONLINE_PEERS" |
    awk '$1 == "node-2" {print $3; exit}')
assert_equals "$ONLINE_STATUS" "ONLINE" \
    "Provider did not begin with an ONLINE public peer status"

e2e_compose stop node-2 >/dev/null

# Discovery is the public operation that attempts the stopped peer and updates
# its observable liveness state.
DISCOVER_STOPPED=$(exec_node node-1 ./proxyma service discover --storage /app/data)
assert_not_contains "$DISCOVER_STOPPED" peer.offline.echo \
    "Stopped provider remained publicly discoverable"

OFFLINE_PEERS=$(exec_node node-1 ./proxyma peers list --storage /app/data)
OFFLINE_STATUS=$(printf '%s\n' "$OFFLINE_PEERS" |
    awk '$1 == "node-2" {print $3; exit}')
assert_equals "$OFFLINE_STATUS" "OFFLINE" \
    "Failed public operation did not mark the stopped provider OFFLINE"

set +e
STOPPED_RUN=$(exec_node node-1 ./proxyma service run \
    --name peer.offline.echo \
    --payload '{"message":"while-stopped"}' \
    --storage /app/data 2>&1)
STOPPED_RUN_EXIT=$?
set -e
if [ "$STOPPED_RUN_EXIT" -eq 0 ]; then
    fail_assertion "Stopped provider remained eligible for service run" "$STOPPED_RUN"
fi
assert_equals "$STOPPED_RUN" \
    "failed to discover service: no nodes available for service 'peer.offline.echo'" \
    "Stopped-provider run returned an unexpected public error"

start_node node-2 8082
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" peer.offline.echo \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

RECOVERED_PEERS=$(exec_node node-1 ./proxyma peers list --storage /app/data)
RECOVERED_STATUS=$(printf '%s\n' "$RECOVERED_PEERS" |
    awk '$1 == "node-2" {print $3; exit}')
assert_equals "$RECOVERED_STATUS" "ONLINE" \
    "Restarted provider did not recover its ONLINE public peer status"

RECOVERED_RUN=$(exec_node node-1 ./proxyma service run \
    --name peer.offline.echo \
    --payload '{"message":"after-restart"}' \
    --storage /app/data)
printf '%s\n' "$RECOVERED_RUN" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
outputs = response.get("outputs", {})
expected = {
    "status": "completed",
    "provider": "node-2",
    "echo": "after-restart",
    "contract": "peer-recovered",
}
actual = {
    "status": response.get("status"),
    "provider": outputs.get("provider"),
    "echo": outputs.get("echo"),
    "contract": outputs.get("contract"),
}
if actual != expected:
    raise SystemExit(f"unexpected recovered response: {actual!r}")
'

echo -e "${GREEN}✅ Case 25 (peer offline visibility and recovery) passed${NC}"
