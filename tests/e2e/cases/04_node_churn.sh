#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_churn"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Abrupt node crash and Churn...${NC}"

install_e2e_case_trap "case-04-failure"
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
MANIFEST_N1=$(wait_for_output "${E2E_VFS_TIMEOUT:-60}" churn_test.txt \
    call_api node-1 GET 8081 manifest)

# Force sync on node-1 to physically download the file
exec_node node-1 ./proxyma storage sync > /dev/null

# Get file hash
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"churn_test.txt":{"name":"churn_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)
assert_not_empty "$FILE_HASH" "churn_test.txt had no public VFS hash"
SOURCE_SHA256=$(sha256sum "$E2E_DATA_DIR/node-2/churn_test.txt" | awk '{print $1}')
assert_equals "$FILE_HASH" "$SOURCE_SHA256" \
    "Public VFS hash did not match the uploaded source"

public_download_hash() {
    local node_id=$1
    local port=$2

    call_api "$node_id" GET "$port" "download/$FILE_HASH" | sha256sum | awk '{print $1}'
}

sponsor_replica_ready() {
    local actual

    exec_node node-1 ./proxyma storage sync >/dev/null 2>&1 || true
    actual=$(public_download_hash node-1 8081 2>&1) || {
        printf '%s\n' "$actual"
        return 1
    }
    printf '%s\n' "$actual"
    [ "$actual" = "$SOURCE_SHA256" ]
}

# Establish and verify an existing replica through the public download API
# before the original source disappears.
echo "🔍 Verifying the complete sponsor replica..."
SPONSOR_SHA256=$(wait_until "${E2E_VFS_TIMEOUT:-60}" \
    "complete public download from sponsor replica" sponsor_replica_ready)
assert_equals "$SPONSOR_SHA256" "$SOURCE_SHA256" \
    "Sponsor replica content did not match the source"

# Prove node-3 has not downloaded yet using its public endpoint.
set +e
call_api node-3 GET 8083 "download/$FILE_HASH" >/dev/null 2>&1
PRELOSS_DOWNLOAD_RC=$?
set -e
if [ "$PRELOSS_DOWNLOAD_RC" -eq 0 ]; then
    fail_assertion "Node 3 already exposed the blob before source loss"
fi

# Remove the source before node-3 starts its download. This validates replica
# fallback deterministically instead of racing a mid-transfer kill.
echo "💥 Killing original source node-2 before client download..."
e2e_compose kill node-2 >/dev/null

echo "📥 Subscribing node-3 after source loss..."
call_api node-3 POST 8083 "subscribe?name=churn_test.txt" >/dev/null

replica_fallback_ready() {
    local actual

    exec_node node-3 ./proxyma storage sync >/dev/null 2>&1 || true
    actual=$(public_download_hash node-3 8083 2>&1) || {
        printf '%s\n' "$actual"
        return 1
    }
    printf '%s\n' "$actual"
    [ "$actual" = "$SOURCE_SHA256" ]
}

echo "🔍 Waiting for node-3 to download from the surviving replica..."
RECOVERED_SHA256=$(wait_until "${E2E_VFS_TIMEOUT:-60}" \
    "exact public download from surviving replica" replica_fallback_ready)
assert_equals "$RECOVERED_SHA256" "$SOURCE_SHA256" \
    "Recovered replica content was corrupted"

echo -e "${GREEN}✅ Recovery by Node Churn successful. File intact.${NC}"
echo -e "${GREEN}🎉 Case 4 (Node Churn) completed successfully!${NC}"
