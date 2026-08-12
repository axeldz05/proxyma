#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_service_remove_restart"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Service removal across provider restart...${NC}"

install_e2e_case_trap "case-28-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-2/schemas"

cat >"$E2E_DATA_DIR/node-2/scripts/removable.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({
    "provider": "node-2",
    "echo": payload.get("message"),
    "contract": "removable-service",
}))
PY
cat >"$E2E_DATA_DIR/node-2/schemas/removable.json" <<'JSON'
{
  "name": "restart.removable",
  "type": "script",
  "description": "Service removal restart contract",
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
    --name restart.removable \
    --type script \
    --exec "python3 /app/data/scripts/removable.py" \
    --schema-file /app/data/schemas/removable.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" restart.removable \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

INITIAL_RUN=$(exec_node node-1 ./proxyma service run \
    --name restart.removable \
    --payload '{"message":"before-remove"}' \
    --storage /app/data)
printf '%s\n' "$INITIAL_RUN" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
outputs = response.get("outputs", {})
expected = {
    "status": "completed",
    "provider": "node-2",
    "echo": "before-remove",
    "contract": "removable-service",
}
actual = {
    "status": response.get("status"),
    "provider": outputs.get("provider"),
    "echo": outputs.get("echo"),
    "contract": outputs.get("contract"),
}
if actual != expected:
    raise SystemExit(f"unexpected pre-removal response: {actual!r}")
'

REMOVE_RESULT=$(exec_node node-2 ./proxyma service remove \
    --name restart.removable --storage /app/data)
assert_equals "$REMOVE_RESULT" \
    "Service 'restart.removable' removed successfully." \
    "Service removal returned an unexpected public result"

restart_node node-2 8082

PROVIDER_DISCOVER=$(exec_node node-2 ./proxyma service discover --storage /app/data)
assert_equals "$PROVIDER_DISCOVER" "No records found." \
    "Removed service reappeared locally after provider restart"

REQUESTER_DISCOVER=$(exec_node node-1 ./proxyma service discover --storage /app/data)
assert_equals "$REQUESTER_DISCOVER" "No records found." \
    "Removed service remained discoverable after provider restart"

set +e
REMOVED_RUN=$(exec_node node-1 ./proxyma service run \
    --name restart.removable \
    --payload '{"message":"after-remove"}' \
    --storage /app/data 2>&1)
REMOVED_RUN_EXIT=$?
set -e
if [ "$REMOVED_RUN_EXIT" -eq 0 ]; then
    fail_assertion "Removed service remained runnable after provider restart" \
        "$REMOVED_RUN"
fi
assert_equals "$REMOVED_RUN" \
    "failed to discover service: no nodes available for service 'restart.removable'" \
    "Removed service run returned an unexpected public error"

FINAL_DISCOVER=$(exec_node node-1 ./proxyma service discover --storage /app/data)
assert_equals "$FINAL_DISCOVER" "No records found." \
    "Removed service did not remain absent after failed run"

echo -e "${GREEN}✅ Case 28 (service removal across restart) passed${NC}"
