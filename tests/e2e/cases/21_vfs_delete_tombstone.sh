#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_vfs_delete_tombstone"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Three-node VFS deletion...${NC}"

cleanup_e2e
finish_case() {
    local exit_code=$?
    trap - EXIT
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "case-21-failure"
    fi
    cleanup_e2e
    exit "$exit_code"
}
trap finish_case EXIT

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

printf '%s\n' "delete-contract-payload" >"$E2E_DATA_DIR/node-1/delete-me.txt"
UPLOAD_RESULT=$(exec_node node-1 ./proxyma storage upload \
    --name delete-me.txt \
    --path /app/data/delete-me.txt \
    --storage /app/data)
assert_contains "$UPLOAD_RESULT" "uploaded successfully" \
    "Requester upload failed"
exec_node node-1 ./proxyma storage sync --storage /app/data >/dev/null

wait_for_output "${E2E_VFS_TIMEOUT:-45}" delete-me.txt \
    call_api node-2 GET 8082 manifest >/dev/null
wait_for_output "${E2E_VFS_TIMEOUT:-45}" delete-me.txt \
    call_api node-3 GET 8083 manifest >/dev/null

MANIFEST=$(call_api node-1 GET 8081 manifest)
FILE_HASH=$(printf '%s\n' "$MANIFEST" | python3 -c '
import json, sys
manifest = json.load(sys.stdin)
print(manifest.get("delete-me.txt", {}).get("hash", ""))
')
assert_not_empty "$FILE_HASH" "Upload manifest did not expose a content hash"

exec_node node-2 ./proxyma storage subscribe \
    --name delete-me.txt --storage /app/data >/dev/null
exec_node node-3 ./proxyma storage subscribe \
    --name delete-me.txt --storage /app/data >/dev/null
exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null
exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null

download_matches() {
    local node_id=$1
    local port=$2
    local output

    output=$(call_api "$node_id" GET "$port" "download/$FILE_HASH" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [ "$output" = "delete-contract-payload" ]
}

wait_until "${E2E_VFS_TIMEOUT:-45}" "node-2 replica download" \
    download_matches node-2 8082 >/dev/null
wait_until "${E2E_VFS_TIMEOUT:-45}" "node-3 replica download" \
    download_matches node-3 8083 >/dev/null

DELETE_RESULT=$(exec_node node-1 ./proxyma storage delete \
    --name delete-me.txt --storage /app/data)
assert_contains "$DELETE_RESULT" "marked as deleted" \
    "Requester delete failed"
exec_node node-1 ./proxyma storage sync --storage /app/data >/dev/null
exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null
exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null

blob_status() {
    local node_id=$1
    local port=$2

    exec_node "$node_id" curl \
        --silent --show-error \
        --connect-timeout "${E2E_HTTP_CONNECT_TIMEOUT:-3}" \
        --max-time "${E2E_HTTP_TIMEOUT:-10}" \
        --cacert /app/data/certs/ca.crt \
        --cert "/app/data/certs/$node_id.crt" \
        --key "/app/data/certs/$node_id.key" \
        --output /dev/null \
        --write-out '%{http_code}' \
        "https://localhost:$port/download/$FILE_HASH"
}

blob_is_gone() {
    local node_id=$1
    local port=$2
    local status

    status=$(blob_status "$node_id" "$port") || return 1
    printf '%s\n' "$status"
    [ "$status" = "404" ]
}

wait_until "${E2E_VFS_TIMEOUT:-45}" "requester blob deletion" \
    blob_is_gone node-1 8081 >/dev/null
wait_until "${E2E_VFS_TIMEOUT:-45}" "node-2 blob deletion" \
    blob_is_gone node-2 8082 >/dev/null
wait_until "${E2E_VFS_TIMEOUT:-45}" "node-3 blob deletion" \
    blob_is_gone node-3 8083 >/dev/null

assert_open_rejected() {
    local node_id=$1
    local output exit_code

    set +e
    output=$(exec_node "$node_id" ./proxyma storage open \
        --name delete-me.txt --storage /app/data 2>&1)
    exit_code=$?
    set -e
    if [ "$exit_code" -eq 0 ]; then
        fail_assertion "$node_id opened a deleted VFS file" "$output"
    fi
}

assert_open_rejected node-2
assert_open_rejected node-3

# Public-contract limitation: `storage list` returns the complete VFS snapshot,
# including deleted rows, and has no file-specific lookup that can return
# not-found. Asserting list absence would require inspecting the tombstone
# representation, which this case deliberately does not do. The case therefore
# narrows the planned list/open/download contract to public open and mTLS
# download unavailability until the CLI offers a live-files-only listing.

echo -e "${GREEN}✅ Case 21 (three-node VFS deletion, narrowed public contract) passed${NC}"
