#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_relay"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Relay Fallback under virtual NAT...${NC}"

install_e2e_case_trap "case-03-failure"
cleanup_e2e

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2"
mkdir -p "$E2E_DATA_DIR/node-3"

# Initialize and bring up cluster
bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

start_node node-1 8081

join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

start_node node-3 8083
start_node node-2 8082

# Register container and network names
NODE1_CONTAINER=$($COMPOSE_CMD ps -q node-1)
NODE3_CONTAINER=$($COMPOSE_CMD ps -q node-3)
DEFAULT_NETWORK="${E2E_PROJECT_NAME}_proxyma-net"
NET_B_NAME="${E2E_PROJECT_NAME}-net-b"

echo "🌐 Creating secondary network $NET_B_NAME..."
docker network create "$NET_B_NAME"

echo "🔗 Connecting node-3 and node-1 to $NET_B_NAME..."
docker network connect --alias node-3 "$NET_B_NAME" "$NODE3_CONTAINER"
docker network connect --alias node-1 "$NET_B_NAME" "$NODE1_CONTAINER"

echo "🚷 Disconnecting node-3 from default network $DEFAULT_NETWORK..."
docker network disconnect "$DEFAULT_NETWORK" "$NODE3_CONTAINER"

# Write and upload a file to node-3
echo "Writing test file on node-3..."
echo "relay_fallback_works" > "$E2E_DATA_DIR/node-3/relay_test.txt"
call_api node-3 POST 8083 upload -F "file=@/app/data/relay_test.txt" > /dev/null

# Repeatedly exercise public sync and manifest behavior until the new topology
# works; no fixed relay-poll settling delay is required.
relay_source_announced() {
    exec_node node-3 ./proxyma storage sync >/dev/null 2>&1 || true
    call_api node-1 GET 8081 manifest
}

# Wait for metadata to propagate to node-1.
echo "🔍 Waiting for metadata propagation from node-3 to node-1..."
MANIFEST_N1=$(wait_for_output "${E2E_VFS_TIMEOUT:-60}" relay_test.txt \
    relay_source_announced)

# Subscribe node-2 to relay_test.txt
call_api node-2 POST 8082 "subscribe?name=relay_test.txt" > /dev/null

# Get file hash
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"relay_test.txt":{"name":"relay_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)
assert_not_empty "$FILE_HASH" "relay_test.txt had no public VFS hash"

relay_download_ready() {
    local output

    exec_node node-2 ./proxyma storage sync >/dev/null 2>&1 || true
    output=$(call_api node-2 GET 8082 "download/$FILE_HASH" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [ "$output" = "relay_fallback_works" ]
}

# Wait directly on the public relay download outcome.
echo "📥 Downloading file on node-2 via Node 1 Relay..."
RELAY_CONTENT=$(wait_until "${E2E_VFS_TIMEOUT:-60}" \
    "exact relay-fallback download content" relay_download_ready)
assert_equals "$RELAY_CONTENT" "relay_fallback_works" \
    "Relay fallback returned unexpected content"

echo -e "${GREEN}✅ Relay Fallback download successful. Content verified.${NC}"
echo -e "${GREEN}🎉 Case 3 (Relay Fallback) completed successfully!${NC}"
