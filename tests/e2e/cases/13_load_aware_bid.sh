#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_load_aware_bid"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Load-aware bid (cheapest prefers idle)...${NC}"

install_e2e_case_trap "case-13-failure"
cleanup_e2e

mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"
mkdir -p "$E2E_DATA_DIR/node-3/scripts"

cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/echo.py"
import sys, json
payload = json.load(sys.stdin)
print(json.dumps({"status": "ok", "from": "node-2", "echo": payload.get("msg", "")}))
EOF

cat << 'EOF' > "$E2E_DATA_DIR/node-3/scripts/echo.py"
import sys, json
payload = json.load(sys.stdin)
print(json.dumps({"status": "ok", "from": "node-3", "echo": payload.get("msg", "")}))
EOF

# Blocking task to saturate workers deterministically (raises EstimatedMillis /
# CostUnits). The FIFO is fixture coordination; container teardown releases it.
mkfifo "$E2E_DATA_DIR/node-3/slow-gate"
cat << 'EOF' > "$E2E_DATA_DIR/node-3/scripts/slow.py"
import pathlib, sys, json
payload = json.load(sys.stdin)
task_id = str(payload.get("x", "unknown"))
pathlib.Path(f"/app/data/slow-{task_id}.started").write_text("started\n")
with open("/app/data/slow-gate", "r", encoding="utf-8") as gate:
    gate.read(1)
print(json.dumps({"status": "ok", "released": True}))
EOF

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

run_node node-2 service add \
    --name "echo" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/echo.py" --desc "echo" --param "msg?:string"
run_node node-3 service add \
    --name "echo" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/echo.py" --desc "echo" --param "msg?:string"
run_node node-3 service add \
    --name "slow" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/slow.py" --desc "block workers" --param "x?:string"

start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" echo \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

echo "Saturating node-3 workers with background slow tasks..."
for i in 1 2 3 4; do
    e2e_compose exec -d node-3 ./proxyma service run \
        --name slow --payload "{\"x\":$i}" --storage /app/data >/dev/null 2>&1 || true
done
wait_until 15 "all slow-task fixtures to begin" \
    exec_node node-3 test \
        -f /app/data/slow-1.started \
        -a -f /app/data/slow-2.started \
        -a -f /app/data/slow-3.started \
        -a -f /app/data/slow-4.started >/dev/null

echo "Running echo with --strategy cheapest (expect idle node-2; retry for sampler noise)..."
idle_provider_selected() {
    local response

    response=$(exec_node node-1 ./proxyma service run \
        --name echo --payload '{"msg":"pick-idle"}' \
        --strategy cheapest --storage /app/data 2>&1) || {
        printf '%s\n' "$response"
        return 1
    }
    printf '%s\n' "$response"
    printf '%s\n' "$response" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
outputs = response.get("outputs", {})
if response.get("status") != "completed" or outputs.get("from") != "node-2":
    raise SystemExit(1)
'
}

STATUS=$(wait_until "${E2E_BID_TIMEOUT:-45}" \
    "cheapest strategy to select idle node-2" idle_provider_selected)
assert_contains "$STATUS" '"from": "node-2"' \
    "Cheapest strategy did not return the idle provider response"

echo -e "${GREEN}✅ Case 13 (load-aware bid) passed — cheapest picked node-2${NC}"
exit 0
