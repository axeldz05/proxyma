#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_outbox_notify"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Offline outbox notify flush...${NC}"

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

mkdir -p "$E2E_DATA_DIR/node-1/scripts" "$E2E_DATA_DIR/node-2"

cat << 'EOF' > "$E2E_DATA_DIR/node-1/scripts/echo.py"
import sys, json
payload = json.load(sys.stdin)
print(json.dumps({"status": "ok", "from": "node-1", "echo": payload.get("msg", "")}))
EOF

bootstrap_node node-1 8081
bootstrap_node node-2 8082

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
$COMPOSE_CMD up -d node-2
sleep 3

NODE2_CONTAINER=$($COMPOSE_CMD ps -q node-2)
NETWORK_NAME="${E2E_PROJECT_NAME}_proxyma-net"

echo "Partition: disconnect node-2..."
docker network disconnect "$NETWORK_NAME" "$NODE2_CONTAINER"

echo "Add service on node-1 while node-2 is partitioned (notify → outbox)..."
exec_node node-1 ./proxyma service add \
    --name "echo_outbox" --storage "/app/data" --type "script" \
    --exec "python3 /app/data/scripts/echo.py" --desc "outbox echo" --param "msg?:string"
sleep 1

echo "Heal partition..."
docker network connect "$NETWORK_NAME" "$NODE2_CONTAINER"
sleep 3

echo "From node-2, run echo_outbox (outbox flush + bid should reach node-1)..."
# Retry until outbox worker flushes and discovery works
ok=0
for i in $(seq 1 15); do
    set +e
    exec_node node-2 ./proxyma service run --name echo_outbox --payload '{"msg":"healed"}' --storage "/app/data" >/dev/null 2>&1
    STATUS=$(exec_node node-2 ./proxyma service status --storage "/app/data" 2>/dev/null)
    set -e
    if echo "$STATUS" | grep -q "completed"; then
        ok=1
        break
    fi
    sleep 1
done

if [ "$ok" -ne 1 ]; then
    echo -e "${RED}❌ echo_outbox did not complete on node-2 after heal${NC}"
    echo "$STATUS"
    exit 1
fi

echo -e "${GREEN}✅ Case 19 (offline outbox notify) passed${NC}"
exit 0
