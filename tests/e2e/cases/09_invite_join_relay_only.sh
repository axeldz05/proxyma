#!/bin/bash
set -eo pipefail

# Join node-3 when it cannot reach the sponsor directly: invite embeds RelayAddr=node-2,
# and the join one-shot runs only on a network shared with the relay (not the sponsor).

export E2E_PROJECT_NAME="e2e_invite_join_relay"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Invite/join via relay only...${NC}"

cleanup_on_exit() {
    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}❌ Test failed with exit code $exit_code. Keeping containers for inspection.${NC}"
    else
        cleanup_e2e
    fi
}
trap cleanup_on_exit EXIT
cleanup_e2e

mkdir -p "$E2E_DATA_DIR/node-1" "$E2E_DATA_DIR/node-2" "$E2E_DATA_DIR/node-3/coverage"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

# Relay forwarding accepts only registered target peers. Readiness of the HTTP
# listener is not enough; wait for both public topology views before turning
# node-2 into the relay advertised by the sponsor.
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-1 node-2
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-2 node-1

# Point sponsor BootstrapNode at node-2 so invites include RelayAddr
echo "🔧 Setting bootstrap_node on sponsor to https://node-2:8082..."
exec_node node-1 python3 - <<'PY'
import json
path = "/app/data/config.json"
with open(path) as f:
    cfg = json.load(f)
cfg["bootstrap_node"] = "https://node-2:8082"
with open(path, "w") as f:
    json.dump(cfg, f, indent=2)
print("updated", cfg.get("bootstrap_node"))
PY

restart_node node-1 8081

NODE2_CONTAINER=$($COMPOSE_CMD ps -q node-2)
NET_B_NAME="${E2E_PROJECT_NAME}-net-b"

echo "🌐 Creating relay-only network $NET_B_NAME (node-2 only; sponsor stays off this net)..."
docker network create "$NET_B_NAME"
docker network connect --alias node-2 "$NET_B_NAME" "$NODE2_CONTAINER"

echo "🎟️ Generating invite on sponsor (expects RelayAddr=node-2)..."
invite_output=$(exec_node node-1 ./proxyma cluster invite)
token=$(echo "$invite_output" | tail -n 1 | tr -d '\r\n ')
if [ -z "$token" ]; then
    echo -e "${RED}❌ Failed to generate invite token${NC}"
    exit 1
fi

echo "🔗 Joining node-3 from relay-only network via docker run (sponsor hostname unreachable)..."
# compose run does not support --network on all versions; use the built image directly.
set +e
JOIN_LOG=$(docker run --rm \
    --network "$NET_B_NAME" \
    --user "${HOST_UID}:${HOST_GID}" \
    -v "${E2E_DATA_DIR}/node-3:/app/data" \
    -e GOCOVERDIR=/app/data/coverage \
    proxyma-e2e-node-3 \
    cluster join --node_id node-3 --token "$token" --port 8083 2>&1)
JOIN_RC=$?
set -e
echo "$JOIN_LOG"

if [ $JOIN_RC -ne 0 ]; then
    echo -e "${RED}❌ Relay-assisted join failed (exit $JOIN_RC)${NC}"
    exit 1
fi

echo "✅ Join command completed. Verifying its public cluster behavior..."
start_node node-3 8083

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-1 \
    call_peer_api node-3 node-1 GET 8081 telemetry >/dev/null

echo "relay_join_ok" > "$E2E_DATA_DIR/node-3/relay_join.txt"
call_api node-3 POST 8083 upload -F "file=@/app/data/relay_join.txt" >/dev/null
exec_node node-3 ./proxyma storage sync >/dev/null || true

wait_for_output "${E2E_VFS_TIMEOUT:-60}" relay_join.txt \
    call_api node-1 GET 8081 manifest >/dev/null

echo -e "${GREEN}✅ Case 09 (invite/join via relay) passed${NC}"
exit 0
