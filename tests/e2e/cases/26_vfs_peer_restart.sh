#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_vfs_peer_restart"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Peer VFS persistence across restart...${NC}"

install_e2e_case_trap "case-26-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2" \
    "$E2E_DATA_DIR/node-3"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083
start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

printf '%s\n' "peer-restart-vfs-content-v1" \
    >"$E2E_DATA_DIR/node-2/peer-restart.txt"
EXPECTED_SHA=$(sha256sum "$E2E_DATA_DIR/node-2/peer-restart.txt" |
    awk '{print $1}')

UPLOAD_RESULT=$(exec_node node-2 ./proxyma storage upload \
    --name peer-restart.txt \
    --path /app/data/peer-restart.txt \
    --storage /app/data)
assert_equals "$UPLOAD_RESULT" \
    "File 'peer-restart.txt' uploaded successfully to VFS." \
    "Peer upload returned an unexpected public result"
exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null

NODE3_MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" peer-restart.txt \
    call_api node-3 GET 8083 manifest)
FILE_HASH=$(printf '%s\n' "$NODE3_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("peer-restart.txt", {}).get("hash", ""))
')
assert_not_empty "$FILE_HASH" \
    "Cross-node manifest exposed no hash before provider restart"
assert_equals "$FILE_HASH" "$EXPECTED_SHA" \
    "Cross-node manifest hash did not match the peer upload"

restart_node node-2 8082

RESTARTED_MANIFEST=$(call_api node-2 GET 8082 manifest)
RESTARTED_HASH=$(printf '%s\n' "$RESTARTED_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("peer-restart.txt", {}).get("hash", ""))
')
assert_equals "$RESTARTED_HASH" "$FILE_HASH" \
    "Restarted peer lost its public VFS manifest entry"

PROVIDER_SHA=$(call_api node-2 GET 8082 "download/$FILE_HASH" |
    sha256sum | awk '{print $1}')
assert_equals "$PROVIDER_SHA" "$FILE_HASH" \
    "Restarted peer lost or corrupted its uploaded blob"

SUBSCRIPTION=$(call_api node-3 POST 8083 "subscribe?name=peer-restart.txt")
assert_contains "$SUBSCRIPTION" "Subscribed to peer-restart.txt" \
    "Cross-node subscriber did not acknowledge the VFS subscription"

cross_node_download_ready() {
    local actual

    exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null 2>&1 || true
    actual=$(call_api node-3 GET 8083 "download/$FILE_HASH" |
        sha256sum | awk '{print $1}') || {
        printf '%s\n' "$actual"
        return 1
    }
    printf '%s\n' "$actual"
    [ "$actual" = "$FILE_HASH" ]
}

CROSS_NODE_SHA=$(wait_until "${E2E_DOWNLOAD_TIMEOUT:-60}" \
    "cross-node download from restarted peer" cross_node_download_ready)
assert_equals "$CROSS_NODE_SHA" "$FILE_HASH" \
    "Cross-node download after peer restart was corrupted"

CROSS_NODE_CONTENT=$(call_api node-3 GET 8083 "download/$FILE_HASH")
assert_equals "$CROSS_NODE_CONTENT" "peer-restart-vfs-content-v1" \
    "Cross-node download returned unexpected content after peer restart"

echo -e "${GREEN}✅ Case 26 (peer VFS restart persistence) passed${NC}"
