#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_churn"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Abrupt node crash and Churn...${NC}"

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

# Subscribe node-1 (the Sponsor) to churn_test.txt to keep a physical copy
echo "📥 Subscribing node-1 (Sponsor) to churn_test.txt..."
call_api node-1 POST 8081 "subscribe?name=churn_test.txt" > /dev/null

# Create a large test file (15 MB) on node-2
echo "📦 Generating large file (15MB) on node-2..."
dd if=/dev/urandom of="$E2E_DATA_DIR/node-2/churn_test.txt" bs=1M count=15 2>/dev/null

# Upload the large file to node-2
echo "📤 Uploading file to node-2..."
call_api node-2 POST 8082 upload -F "file=@/app/data/churn_test.txt" > /dev/null

# Force sync on node-2 to announce the file to the Sponsor
exec_node node-2 ./proxyma storage sync > /dev/null

# Wait for the file to reach the VFS of node-1 and be downloaded
echo "🔍 Waiting for node-1 to detect and download the file..."
if ! wait_for_condition 15 2 "churn_test.txt" call_api node-1 GET 8081 manifest; then
    echo -e "${RED}❌ Error: The Sponsor did not receive the file metadata.${NC}"
    exit 1
fi

# Force sync on node-1 to physically download the file
exec_node node-1 ./proxyma storage sync > /dev/null

# Get file hash
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"churn_test.txt":{"name":"churn_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

if [ -z "$FILE_HASH" ]; then
    echo -e "${RED}❌ Error: Could not get the file hash${NC}"
    exit 1
fi

# Verify physical download on node-1
echo "🔍 Verifying physical copy on node-1..."
if ! wait_for_condition 10 1 "$FILE_HASH" exec_node node-1 ls "/app/data/$FILE_HASH"; then
    echo -e "${RED}❌ Error: The Sponsor did not download the physical copy of the file.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ The Sponsor (node-1) has the complete physical copy of the file.${NC}"

# Subscribe node-3 (the client) to the file
echo "📥 Subscribing node-3 to the file..."
call_api node-3 POST 8083 "subscribe?name=churn_test.txt" > /dev/null

# Start synchronization on node-3 in the background (to interrupt it mid-download)
echo "⚡ Starting synchronization on node-3 in the background..."
exec_node node-3 ./proxyma storage sync > /dev/null &
SYNC_PID=$!

# Wait a brief moment for the download to start from node-2
sleep 1.5

# Kill node-2 abruptly
echo "💥 Killing node-2 abruptly (simulating power cut)..."
$COMPOSE_CMD kill node-2 >/dev/null

# Wait for the background command to finish or fail
wait $SYNC_PID || true

# Since node-2 died in the middle, synchronization on node-3 might have failed
# or remained incomplete. Let's force another synchronization on node-3.
# With node-2 gone, node-3 should only be able to download the rest/all from node-1 (the Sponsor).
echo "🔄 Forcing recovery synchronization on node-3..."
exec_node node-3 ./proxyma storage sync > /dev/null || true

# Wait for node-3 to finish download and verify physical integrity
echo "🔍 Waiting for node-3 to complete the download from node-1..."
if ! wait_for_condition 15 2 "$FILE_HASH" exec_node node-3 ls "/app/data/$FILE_HASH"; then
    echo -e "${RED}❌ Error: node-3 could not recover the file after node-2 crashed.${NC}"
    exit 1
fi

# Download and verify binary integrity
call_api node-3 GET 8083 "download/$FILE_HASH" > "$E2E_DATA_DIR/node-3/downloaded_churn.txt"
if ! diff "$E2E_DATA_DIR/node-2/churn_test.txt" "$E2E_DATA_DIR/node-3/downloaded_churn.txt" > /dev/null; then
    echo -e "${RED}❌ Error: The recovered file on node-3 is corrupted.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Recovery by Node Churn successful. File intact.${NC}"
echo -e "${GREEN}🎉 Case 4 (Node Churn) completed successfully!${NC}"
