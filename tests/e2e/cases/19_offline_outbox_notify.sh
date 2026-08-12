#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_outbox_notify"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Durable offline outbox notify...${NC}"

install_e2e_case_trap "case-19-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1/scripts" \
    "$E2E_DATA_DIR/node-1/schemas" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-2/schemas"

cat >"$E2E_DATA_DIR/node-1/scripts/echo.py" <<'PY'
import json
import sys

payload = json.load(sys.stdin)
print(json.dumps({
    "status": "ok",
    "from": "node-1",
    "echo": payload.get("msg"),
    "contract": "healed-after-producer-restart"
}))
PY
cat >"$E2E_DATA_DIR/node-2/scripts/source.py" <<'PY'
import json
print(json.dumps({"text": "source-text"}))
PY

cat >"$E2E_DATA_DIR/node-1/schemas/echo.json" <<'JSON'
{
  "name": "echo_outbox",
  "type": "script",
  "description": "Durable outbox echo",
  "parameters": {
    "msg": {"type": "int", "required": false}
  },
  "outputs": {
    "contract": {"type": "string"}
  }
}
JSON
cat >"$E2E_DATA_DIR/node-2/schemas/source.json" <<'JSON'
{
  "name": "outbox.source",
  "type": "script",
  "parameters": {},
  "outputs": {
    "text": {"type": "string"}
  }
}
JSON

bootstrap_node node-1 8081
bootstrap_node node-2 8082
run_node node-2 service add \
    --name outbox.source \
    --type script \
    --exec "python3 /app/data/scripts/source.py" \
    --schema-file /app/data/schemas/source.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

exec_node node-2 ./proxyma service subscribe \
    --name echo_outbox --storage /app/data >/dev/null

wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-1 node-2

disconnect_node node-2

exec_node node-1 ./proxyma service add \
    --name echo_outbox \
    --type script \
    --exec "python3 /app/data/scripts/echo.py" \
    --schema-file /app/data/schemas/echo.json \
    --storage /app/data >/dev/null

# The mutation has durably staged its notify before this command returns.
# Restarting now proves the producer cannot depend on in-memory retry state.
restart_node node-1 8081

wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-1 node-2

reconnect_node node-2

OUTBOX_MISMATCH='{"id":"outbox-offline-validation","version":1,"steps":[{"id":"source","service":"outbox.source"},{"id":"target","service":"echo_outbox"}],"connections":[{"from_step":"source","from_port":"text","to_step":"target","to_port":"msg"}]}'

outbox_schema_delivered() {
    local output

    if output=$(exec_node node-2 ./proxyma service add_pipeline \
        --id outbox-offline-validation \
        --schema "$OUTBOX_MISMATCH" \
        --storage /app/data 2>&1); then
        printf '%s\n' "$output"
        return 1
    fi
    printf '%s\n' "$output"
    [[ "$output" == *"type mismatch"* ]]
}

# Offline pipeline validation enforces the remote parameter type once the
# subscribed schema notification arrives; live service discovery is not used.
wait_until "${E2E_OUTBOX_TIMEOUT:-45}" \
    "subscribed schema validation after producer restart" \
    outbox_schema_delivered >/dev/null
exec_node node-2 ./proxyma service remove_pipeline \
    --id outbox-offline-validation --storage /app/data >/dev/null 2>&1 || true

RUN_RESULT=$(exec_node node-2 ./proxyma service run \
    --name echo_outbox --payload '{"msg":7}' --storage /app/data)
assert_contains "$RUN_RESULT" '"status": "completed"' \
    "Healed producer service did not complete"
assert_contains "$RUN_RESULT" '"contract": "healed-after-producer-restart"' \
    "Healed producer service returned unexpected output"

echo -e "${GREEN}✅ Case 19 (durable outbox across producer restart) passed${NC}"
