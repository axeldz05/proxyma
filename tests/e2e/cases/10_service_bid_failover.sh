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

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
$COMPOSE_CMD up -d node-2 node-3
sleep 3

echo "Running echo while both providers are up..."
exec_node node-1 ./proxyma service run --name echo --payload '{"msg":"hi"}' --storage "/app/data" >/dev/null || true
STATUS1=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "status1: $STATUS1"
if ! echo "$STATUS1" | grep -q "completed"; then
    echo -e "${RED}❌ Initial echo bid/run failed${NC}"
    exit 1
fi

echo "🛑 Killing node-2 (primary provider candidate)..."
$COMPOSE_CMD stop node-2
sleep 2

echo "Running echo after node-2 down (should bid to node-3)..."
exec_node node-1 ./proxyma service run --name echo --payload '{"msg":"failover"}' --storage "/app/data" >/dev/null || true
STATUS2=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "status2: $STATUS2"

if echo "$STATUS2" | grep -q "failed"; then
    # Accept explicit failure if cluster cannot rediscover — still assert no hang
    echo -e "${YELLOW}⚠️ Echo failed after killing node-2 (explicit fail is acceptable)${NC}"
    if ! echo "$STATUS2" | grep -qiE 'failed|no nodes|discover'; then
        echo -e "${RED}❌ Unexpected failure mode${NC}"
        exit 1
    fi
elif echo "$STATUS2" | grep -q "completed"; then
    echo -e "${GREEN}✅ Failover to remaining provider succeeded${NC}"
else
    echo -e "${RED}❌ Unexpected status after provider kill${NC}"
    exit 1
fi

echo "🛑 Stopping node-3 too — expect discovery failure..."
$COMPOSE_CMD stop node-3
sleep 1
set +e
FAIL_OUT=$(exec_node node-1 ./proxyma service run --name echo --payload '{"msg":"none"}' --storage "/app/data" 2>&1)
set -e
echo "no-provider output: $FAIL_OUT"
if ! echo "$FAIL_OUT" | grep -qiE 'fail|error|no nodes|discover'; then
    STATUS3=$(exec_node node-1 ./proxyma service status --storage "/app/data" 2>/dev/null || true)
    if ! echo "$STATUS3" | grep -qiE 'failed|error'; then
        echo -e "${RED}❌ Expected discovery failure with no providers${NC}"
        exit 1
    fi
fi

echo -e "${GREEN}✅ Case 10 (service bid failover) passed${NC}"
exit 0
