#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_bid_failover"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Service bid failover...${NC}"

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
cp "$E2E_DATA_DIR/node-2/scripts/echo.py" "$E2E_DATA_DIR/node-3/scripts/echo.py"
# node-3 reports different origin
sed -i 's/node-2/node-3/g' "$E2E_DATA_DIR/node-3/scripts/echo.py"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

run_node node-2 service add \
    --name "echo" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/echo.py" --desc "echo" --param "msg?:string"
run_node node-3 service add \
    --name "echo" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/echo.py" --desc "echo" --param "msg?:string"

start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" echo \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

echo "Running echo while both providers are up..."
INITIAL_OUT=$(exec_node node-1 ./proxyma service run \
    --name echo --payload '{"msg":"hi"}' --storage /app/data)
assert_contains "$INITIAL_OUT" '"status": "completed"' \
    "Initial echo bid/run did not complete"

echo "🛑 Killing node-2 (primary provider candidate)..."
e2e_compose stop node-2 >/dev/null

echo "Running echo after node-2 down (should bid to node-3)..."
FAILOVER_OUT=$(exec_node node-1 ./proxyma service run \
    --name echo --payload '{"msg":"failover"}' --storage /app/data)
echo "failover response: $FAILOVER_OUT"
printf '%s\n' "$FAILOVER_OUT" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
expected = {
    "status": "completed",
    "from": "node-3",
    "echo": "failover",
}
actual = {
    "status": response.get("status"),
    "from": response.get("outputs", {}).get("from"),
    "echo": response.get("outputs", {}).get("echo"),
}
if actual != expected:
    raise SystemExit(f"unexpected surviving-provider response: {actual!r}")
'
echo -e "${GREEN}✅ Failover returned the exact node-3 response${NC}"

echo "🛑 Stopping node-3 too — expect discovery failure..."
e2e_compose stop node-3 >/dev/null
set +e
FAIL_OUT=$(exec_node node-1 ./proxyma service run --name echo --payload '{"msg":"none"}' --storage "/app/data" 2>&1)
FAIL_RC=$?
set -e
echo "no-provider output: $FAIL_OUT"
if [ "$FAIL_RC" -eq 0 ]; then
    fail_assertion "Service run succeeded with no providers" "$FAIL_OUT"
fi
assert_contains "$FAIL_OUT" "no nodes available for service 'echo'" \
    "No-provider run returned an unexpected error"

echo -e "${GREEN}✅ Case 10 (service bid failover) passed${NC}"
exit 0
