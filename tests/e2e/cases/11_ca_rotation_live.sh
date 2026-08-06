#!/bin/bash
set -eo pipefail

# Trigger CA rotation by peer leave on the sponsor, wait until TLS is healthy again,
# then verify VFS sync still works between remaining nodes.

export E2E_PROJECT_NAME="e2e_ca_rotation_live"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: CA rotation under live traffic...${NC}"

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

mkdir -p "$E2E_DATA_DIR/node-1" "$E2E_DATA_DIR/node-2" "$E2E_DATA_DIR/node-3"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
$COMPOSE_CMD up -d node-2 node-3
sleep 4

# Warm mTLS so sponsor caches peer leaf certs (needed to re-sign on rotation)
call_api node-2 GET 8082 peers >/dev/null || true
call_api node-3 GET 8083 peers >/dev/null || true
call_api node-1 GET 8081 peers >/dev/null || true
# Touch sponsor from peers (client cert presented → SetPeerCertificate)
exec_node node-2 ./proxyma storage sync >/dev/null || true
exec_node node-3 ./proxyma storage sync >/dev/null || true
sleep 2

# Seed a file before rotation
echo "pre_rotation" > "$E2E_DATA_DIR/node-1/pre.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/pre.txt" >/dev/null
exec_node node-2 ./proxyma storage sync >/dev/null || true

if ! wait_for_condition 15 2 "pre.txt" call_api node-2 GET 8082 manifest; then
    echo -e "${RED}❌ pre-rotation sync failed${NC}"
    exit 1
fi

CA_BEFORE=$(exec_node node-1 sha256sum /app/data/certs/ca.crt | awk '{print $1}')
echo "CA before leave/rotation: $CA_BEFORE"

# Only the sponsor leave path triggers RotateCAAndResignPeers with CA authority.
# Rotation runs in a goroutine — do not race uploads against in-memory TLS reload.
echo "👋 Removing node-3 via sponsor leave (triggers CA rotation)..."
set +e
LEAVE_OUT=$(call_api node-1 POST 8081 peers/leave -H "Content-Type: application/json" -d '{"id":"node-3"}' 2>&1)
LEAVE_RC=$?
set -e
echo "leave response (rc=$LEAVE_RC): $LEAVE_OUT"
$COMPOSE_CMD stop node-3 >/dev/null || true

echo "⏳ Waiting for CA file rotation on sponsor..."
CA_AFTER=""
for i in $(seq 1 30); do
    CA_AFTER=$(exec_node node-1 sha256sum /app/data/certs/ca.crt 2>/dev/null | awk '{print $1}')
    if [ -n "$CA_AFTER" ] && [ "$CA_AFTER" != "$CA_BEFORE" ]; then
        break
    fi
    sleep 1
done
echo "CA after leave/rotation: $CA_AFTER"
if [ -z "$CA_AFTER" ] || [ "$CA_AFTER" = "$CA_BEFORE" ]; then
    echo -e "${RED}❌ CA did not rotate on sponsor${NC}"
    exit 1
fi

echo "⏳ Waiting for node-2 to receive rotated CA..."
if ! wait_for_condition 30 2 "$CA_AFTER" exec_node node-2 sha256sum /app/data/certs/ca.crt; then
    echo -e "${RED}❌ node-2 CA was not updated after rotation${NC}"
    exec_node node-2 sha256sum /app/data/certs/ca.crt || true
    exit 1
fi

echo "⏳ Waiting until mTLS endpoints accept the new certs..."
tls_ok() {
    local node=$1 port=$2
    local out
    out=$(call_api "$node" GET "$port" telemetry 2>/dev/null || true)
    echo "$out" | grep -q "node_id"
}
for i in $(seq 1 30); do
    if tls_ok node-1 8081 && tls_ok node-2 8082; then
        break
    fi
    sleep 1
done
if ! tls_ok node-1 8081 || ! tls_ok node-2 8082; then
    echo -e "${RED}❌ Telemetry/mTLS still unhealthy after rotation${NC}"
    call_api node-1 GET 8081 telemetry || true
    call_api node-2 GET 8082 telemetry || true
    exit 1
fi

# Live traffic after rotation: upload from node-1, sync to node-2
echo "post_rotation" > "$E2E_DATA_DIR/node-1/post.txt"
set +e
UP_OUT=$(call_api node-1 POST 8081 upload -F "file=@/app/data/post.txt" 2>&1)
UP_RC=$?
set -e
echo "upload after rotation (rc=$UP_RC): $UP_OUT"
if [ $UP_RC -ne 0 ]; then
    echo -e "${RED}❌ upload failed after CA rotation${NC}"
    exit 1
fi

exec_node node-1 ./proxyma storage sync >/dev/null || true
exec_node node-2 ./proxyma storage sync >/dev/null || true

if ! wait_for_condition 20 2 "post.txt" call_api node-2 GET 8082 manifest; then
    echo -e "${RED}❌ post-rotation VFS sync failed between remaining peers${NC}"
    call_api node-1 GET 8081 manifest || true
    call_api node-2 GET 8082 manifest || true
    exit 1
fi

PROBE=$(call_api node-1 GET 8081 peers || true)
echo "peers after rotation: $PROBE"

echo -e "${GREEN}✅ Case 11 (CA rotation live) passed${NC}"
exit 0
