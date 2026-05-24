#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_partition"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Network Partition...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

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

# Register container names and network
NODE3_CONTAINER=$(docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE ps -q node-3)
NETWORK_NAME="${E2E_PROJECT_NAME}_proxyma-net"

# 1. Verify synchronization in normal network
echo "Writing initial file to verify initial state..."
echo "base_state" > "$E2E_DATA_DIR/node-1/base.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/base.txt" > /dev/null

exec_node node-2 ./proxyma sync > /dev/null
exec_node node-3 ./proxyma sync > /dev/null

# Confirm base state
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if ! echo "$MANIFEST_N3" | grep -q "base.txt"; then
    echo -e "${RED}❌ Error: Base synchronization failed${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Base state verified on all nodes.${NC}"

# 2. Cause partition: Isolate node-3
echo "Disconnecting node-3 from network $NETWORK_NAME..."
docker network disconnect "$NETWORK_NAME" "$NODE3_CONTAINER"

# 3. Divergent writes in the partition
echo "Writing file A in the connected partition (node-1)..."
echo "data_partition_a" > "$E2E_DATA_DIR/node-1/partition_a.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/partition_a.txt" > /dev/null

echo "Writing file B in the isolated node (node-3)..."
echo "data_partition_b" > "$E2E_DATA_DIR/node-3/partition_b.txt"
# Note: The API call is local inside the node-3 container, so it works even without external network
call_api node-3 POST 8083 upload -F "file=@/app/data/partition_b.txt" > /dev/null

# 4. Sync and verify that information does NOT propagate
echo "Attempting to sync while the partition exists..."
exec_node node-2 ./proxyma sync > /dev/null || true
# Note: This may fail or pass without error but without updating node-3, which is correct.

# Verify that node-1/2 do not know about partition_b.txt
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
if echo "$MANIFEST_N1" | grep -q "partition_b.txt"; then
    echo -e "${RED}❌ Error: Data leak across the partition (node-1 knows partition_b.txt)${NC}"
    exit 1
fi

# Verify that node-3 does not know about partition_a.txt
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if echo "$MANIFEST_N3" | grep -q "partition_a.txt"; then
    echo -e "${RED}❌ Error: Data leak across the partition (node-3 knows partition_a.txt)${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Nodes isolated correctly. No metadata transfer.${NC}"

# 5. Heal partition: Connect node-3
echo "Reconnecting node-3 to network $NETWORK_NAME..."
docker network connect "$NETWORK_NAME" "$NODE3_CONTAINER"
sleep 2

# 6. Sync healed cluster
echo "Triggering synchronization after reconnection..."
exec_node node-3 ./proxyma sync > /dev/null
exec_node node-1 ./proxyma sync > /dev/null

# 7. Verify convergence
echo "🔍 Verifying metadata convergence on node-1..."
if ! wait_for_condition 10 2 "partition_a.txt" call_api node-1 GET 8081 manifest || \
   ! wait_for_condition 10 2 "partition_b.txt" call_api node-1 GET 8081 manifest; then
    echo -e "${RED}❌ Error: The cluster did not recover consistency after healing the partition.${NC}"
    exit 1
fi

# Verify on node-3
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if ! echo "$MANIFEST_N3" | grep -q "partition_a.txt" || ! echo "$MANIFEST_N3" | grep -q "partition_b.txt"; then
    echo -e "${RED}❌ Error: The reconnected node 3 did not receive the global metadata.${NC}"
    exit 1
fi

echo -e "${GREEN}🎉 Case 2 (Network Partition) completed successfully!${NC}"
