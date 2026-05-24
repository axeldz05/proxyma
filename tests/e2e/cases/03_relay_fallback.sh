#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_relay"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Relay Fallback under virtual NAT...${NC}"

cleanup_on_exit() {
    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}❌ Test failed with exit code $exit_code. Keeping containers for inspection.${NC}"
    else
        cleanup_e2e
    fi
}
trap cleanup_on_exit EXIT

# Initial cleanup
cleanup_e2e

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2"
mkdir -p "$E2E_DATA_DIR/node-3"

# Initialize and bring up cluster
bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

$COMPOSE_CMD up -d node-1
sleep 2

join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

$COMPOSE_CMD up -d node-2 node-3
sleep 2

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

# Wait for network topology to settle and node-3 to reestablish polling on the new network
sleep 20

# Write and upload a file to node-3
echo "Writing test file on node-3..."
echo "relay_fallback_works" > "$E2E_DATA_DIR/node-3/relay_test.txt"
call_api node-3 POST 8083 upload -F "file=@/app/data/relay_test.txt" > /dev/null

# Force sync on node-3 so it announces metadata to sponsor (node-1)
exec_node node-3 ./proxyma sync > /dev/null

# Wait for metadata to propagate to node-1
echo "🔍 Waiting for metadata propagation from node-3 to node-1..."
if ! wait_for_condition 10 2 "relay_test.txt" call_api node-1 GET 8081 manifest; then
    echo -e "${RED}❌ Error: Metadata from node-3 did not reach node-1${NC}"
    exit 1
fi

# Subscribe node-2 to relay_test.txt
call_api node-2 POST 8082 "subscribe?name=relay_test.txt" > /dev/null

# Force sync on node-2
exec_node node-2 ./proxyma sync > /dev/null

# Get file hash
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"relay_test.txt":{"name":"relay_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

if [ -z "$FILE_HASH" ]; then
    echo -e "${RED}❌ Error: Could not find the hash of relay_test.txt in the manifest${NC}"
    exit 1
fi

# Wait for download on node-2 via node-1 relay
echo "📥 Downloading file on node-2 via Node 1 Relay..."
if ! wait_for_condition 15 2 "relay_fallback_works" call_api node-2 GET 8082 "download/$FILE_HASH"; then
    echo -e "${RED}❌ Error: Relay Fallback download failed or did not complete.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Relay Fallback download successful. Content verified.${NC}"
echo -e "${GREEN}🎉 Case 3 (Relay Fallback) completed successfully!${NC}"
