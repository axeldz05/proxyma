#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_partition"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Network Partition...${NC}"

install_e2e_case_trap "case-02-failure"
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

start_nodes node-2 node-3

# Register container names and network
NODE3_CONTAINER=$(docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE ps -q node-3)
NETWORK_NAME="${E2E_PROJECT_NAME}_proxyma-net"

# 1. Verify synchronization in normal network
echo "Writing initial file to verify initial state..."
echo "base_state" > "$E2E_DATA_DIR/node-1/base.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/base.txt" > /dev/null

exec_node node-2 ./proxyma storage sync > /dev/null
exec_node node-3 ./proxyma storage sync > /dev/null

# Confirm base state
MANIFEST_N3=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" base.txt \
    call_api node-3 GET 8083 manifest)
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
exec_node node-2 ./proxyma storage sync > /dev/null || true
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

# 6. Sync healed cluster. Repeated public sync/list operations absorb Docker DNS
# and route convergence without a fixed network-settling delay.
echo "Triggering synchronization after reconnection..."
healed_cluster_converged() {
    local manifest_node1 manifest_node3

    exec_node node-3 ./proxyma storage sync >/dev/null 2>&1 || true
    exec_node node-1 ./proxyma storage sync >/dev/null 2>&1 || true
    manifest_node1=$(call_api node-1 GET 8081 manifest 2>&1) || {
        printf '%s\n' "$manifest_node1"
        return 1
    }
    manifest_node3=$(call_api node-3 GET 8083 manifest 2>&1) || {
        printf '%s\n' "$manifest_node3"
        return 1
    }
    printf '%s\n%s\n' "$manifest_node1" "$manifest_node3"
    [[ "$manifest_node1" == *partition_a.txt* &&
        "$manifest_node1" == *partition_b.txt* &&
        "$manifest_node3" == *partition_a.txt* &&
        "$manifest_node3" == *partition_b.txt* ]]
}

wait_until "${E2E_VFS_TIMEOUT:-60}" "public VFS convergence after partition heal" \
    healed_cluster_converged >/dev/null

# 7. Verify convergence
echo "🔍 Verifying metadata convergence on node-1..."
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
assert_contains "$MANIFEST_N1" partition_a.txt \
    "Healed node-1 manifest omitted partition_a.txt"
assert_contains "$MANIFEST_N1" partition_b.txt \
    "Healed node-1 manifest omitted partition_b.txt"

# Verify on node-3
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
assert_contains "$MANIFEST_N3" partition_a.txt \
    "Healed node-3 manifest omitted partition_a.txt"
assert_contains "$MANIFEST_N3" partition_b.txt \
    "Healed node-3 manifest omitted partition_b.txt"

echo -e "${GREEN}🎉 Case 2 (Network Partition) completed successfully!${NC}"
