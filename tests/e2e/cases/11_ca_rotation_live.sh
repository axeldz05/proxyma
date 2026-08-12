#!/bin/bash
set -euo pipefail

# Trigger CA rotation by peer leave on the sponsor, wait until TLS is healthy again,
# then verify VFS sync still works between remaining nodes.

export E2E_PROJECT_NAME="e2e_ca_rotation_live"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: CA rotation under live traffic...${NC}"

install_e2e_case_trap "case-11-failure"
cleanup_e2e

mkdir -p "$E2E_DATA_DIR/node-1" "$E2E_DATA_DIR/node-2" "$E2E_DATA_DIR/node-3"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

# Exercise a real peer-to-peer mTLS request so the sponsor has observed the
# remaining peer's public TLS identity before rotation.
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-1 \
    call_peer_api node-2 node-1 GET 8081 telemetry >/dev/null

live_peer_serial() {
    local client_id=$1
    local target_host=$2
    local target_port=$3

    exec_node "$client_id" python3 -c '
import socket
import ssl
import sys

client_id, host, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
context = ssl.create_default_context(cafile="/app/data/certs/ca.crt")
context.load_cert_chain(
    certfile=f"/app/data/certs/{client_id}.crt",
    keyfile=f"/app/data/certs/{client_id}.key",
)
with socket.create_connection((host, port), timeout=5) as raw:
    with context.wrap_socket(raw, server_hostname=host) as tls:
        print(tls.getpeercert()["serialNumber"])
' "$client_id" "$target_host" "$target_port"
}

TLS_SERIAL_BEFORE=$(live_peer_serial node-2 node-1 8081)
assert_not_empty "$TLS_SERIAL_BEFORE" \
    "Live pre-rotation peer handshake exposed no TLS serial"
echo "Live sponsor TLS serial before rotation: $TLS_SERIAL_BEFORE"

# Seed a file before rotation
echo "pre_rotation" > "$E2E_DATA_DIR/node-1/pre.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/pre.txt" >/dev/null
exec_node node-2 ./proxyma storage sync >/dev/null || true

wait_for_output "${E2E_VFS_TIMEOUT:-45}" pre.txt \
    call_api node-2 GET 8082 manifest >/dev/null

# Only the sponsor leave path triggers RotateCAAndResignPeers with CA authority.
# Rotation runs asynchronously; observe it through the public peer handshake.
echo "👋 Removing node-3 via sponsor leave (triggers CA rotation)..."
set +e
LEAVE_OUT=$(call_api node-1 POST 8081 peers/leave -H "Content-Type: application/json" -d '{"id":"node-3"}' 2>&1)
LEAVE_RC=$?
set -e
echo "leave response (rc=$LEAVE_RC): $LEAVE_OUT"
if [ "$LEAVE_RC" -ne 0 ]; then
    fail_assertion "Sponsor rejected the public leave request" "$LEAVE_OUT"
fi
e2e_compose stop node-3 >/dev/null || true

rotated_peer_identity_ready() {
    local serial

    serial=$(live_peer_serial node-2 node-1 8081 2>&1) || {
        printf '%s\n' "$serial"
        return 1
    }
    printf '%s\n' "$serial"
    [ "$serial" != "$TLS_SERIAL_BEFORE" ]
}

echo "⏳ Waiting for a changed live TLS identity and valid peer handshake..."
TLS_SERIAL_AFTER=$(wait_until "${E2E_TLS_TIMEOUT:-60}" \
    "rotated sponsor TLS identity accepted by node-2" \
    rotated_peer_identity_ready)
assert_not_empty "$TLS_SERIAL_AFTER" \
    "Live post-rotation peer handshake exposed no TLS serial"
if [ "$TLS_SERIAL_AFTER" = "$TLS_SERIAL_BEFORE" ]; then
    fail_assertion "Sponsor TLS identity did not rotate" "$TLS_SERIAL_AFTER"
fi
echo "Live sponsor TLS serial after rotation: $TLS_SERIAL_AFTER"

wait_for_output "${E2E_TLS_TIMEOUT:-60}" node-1 \
    call_peer_api node-2 node-1 GET 8081 telemetry >/dev/null

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

call_api node-2 POST 8082 "subscribe?name=post.txt" >/dev/null
post_rotation_visible() {
    exec_node node-1 ./proxyma storage sync >/dev/null 2>&1 || true
    exec_node node-2 ./proxyma storage sync >/dev/null 2>&1 || true
    call_api node-2 GET 8082 manifest
}
POST_MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-60}" post.txt \
    post_rotation_visible)
POST_HASH=$(echo "$POST_MANIFEST" | grep -o '"post.txt":{"name":"post.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)
assert_not_empty "$POST_HASH" "post.txt had no public VFS hash after rotation"
POST_CONTENT=$(wait_for_output "${E2E_VFS_TIMEOUT:-60}" post_rotation \
    call_api node-2 GET 8082 "download/$POST_HASH")
assert_equals "$POST_CONTENT" post_rotation \
    "Post-rotation peer operation returned incorrect content"

PROBE=$(call_api node-1 GET 8081 peers || true)
echo "peers after rotation: $PROBE"

echo -e "${GREEN}✅ Case 11 (CA rotation live) passed${NC}"
exit 0
