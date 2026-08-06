#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_load_aware_bid"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Load-aware bid (cheapest prefers idle)...${NC}"

cleanup_on_exit() {
    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}❌ Test failed. Keeping containers.${NC}"
    else
        cleanup_e2e
    fi
}
trap cleanup_on_exit EXIT
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

# Long-running task to saturate workers (raises EstimatedMillis / CostUnits)
cat << 'EOF' > "$E2E_DATA_DIR/node-3/scripts/slow.py"
import sys, json, time
payload = json.load(sys.stdin)
time.sleep(25)
print(json.dumps({"status": "ok", "slept": True}))
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

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
$COMPOSE_CMD up -d node-2 node-3
sleep 3

echo "Saturating node-3 workers with background slow tasks..."
for i in 1 2 3 4; do
    $COMPOSE_CMD exec -d node-3 ./proxyma service run --name slow --payload "{\"x\":$i}" --storage "/app/data" >/dev/null 2>&1 || true
done
sleep 2

echo "Running echo with --strategy cheapest (expect idle node-2)..."
exec_node node-1 ./proxyma service run --name echo --payload '{"msg":"pick-idle"}' --strategy cheapest --storage "/app/data" >/dev/null || true
STATUS=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "status: $STATUS"

if ! echo "$STATUS" | grep -q '"from": "node-2"'; then
    # Also accept nested JSON without spaces
    if ! echo "$STATUS" | grep -qE '"from"[[:space:]]*:[[:space:]]*"node-2"'; then
        echo -e "${RED}❌ Expected echo on idle node-2 under cheapest strategy${NC}"
        exit 1
    fi
fi
if echo "$STATUS" | grep -q '"from": "node-3"'; then
    echo -e "${RED}❌ Task landed on saturated node-3${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Case 13 (load-aware bid) passed — cheapest picked node-2${NC}"
exit 0
