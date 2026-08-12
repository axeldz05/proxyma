#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_durable_download_restart"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Durable download intent restart...${NC}"

cleanup_e2e
finish_case() {
    local exit_code=$?
    trap - EXIT
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "case-24-failure"
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

printf '%s\n' "durable-download-after-restart" \
    >"$E2E_DATA_DIR/node-2/durable-restart.txt"
UPLOAD_RESULT=$(exec_node node-2 ./proxyma storage upload \
    --name durable-restart.txt \
    --path /app/data/durable-restart.txt \
    --storage /app/data)
assert_contains "$UPLOAD_RESULT" "uploaded successfully" \
    "Source upload failed"

exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null
MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" durable-restart.txt \
    call_api node-1 GET 8081 manifest)
FILE_HASH=$(printf '%s\n' "$MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("durable-restart.txt", {}).get("hash", ""))
')
assert_not_empty "$FILE_HASH" \
    "Sponsor manifest did not expose the uploaded content hash"

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

# Stop the only physical source. Node-1 remains reachable for metadata sync.
e2e_compose stop node-2 >/dev/null

exec_node node-3 ./proxyma storage subscribe \
    --name durable-restart.txt --storage /app/data >/dev/null
exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null

VFS_LIST=$(exec_node node-3 ./proxyma storage list --storage /app/data)
assert_contains "$VFS_LIST" durable-restart.txt \
    "Requester did not list the file before restart"

PRE_RESTART_STATUS=$(blob_status node-3 8083)
assert_equals "$PRE_RESTART_STATUS" 404 \
    "Requester exposed the blob while the source was offline"

# Restart the requester while the source remains offline.
restart_node node-3 8083

POST_RESTART_STATUS=$(blob_status node-3 8083)
assert_equals "$POST_RESTART_STATUS" 404 \
    "Restarted requester exposed the blob while the source remained offline"

start_node node-2 8082

download_matches() {
    local output

    output=$(call_api node-3 GET 8083 "download/$FILE_HASH" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [ "$output" = "durable-download-after-restart" ]
}

# No post-restart `storage sync` is issued: recovery must remain automatic.
wait_until "${E2E_DOWNLOAD_TIMEOUT:-60}" \
    "durable download recovery after subscriber restart" \
    download_matches >/dev/null

FINAL_LIST=$(exec_node node-3 ./proxyma storage list --storage /app/data)
assert_contains "$FINAL_LIST" durable-restart.txt \
    "Recovered file disappeared from the public VFS listing"

echo -e "${GREEN}✅ Case 24 (durable download intent across restart) passed${NC}"
