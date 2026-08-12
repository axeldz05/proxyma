#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_vfs_unsubscribe_purge"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: VFS unsubscribe, purge, and reopen...${NC}"

install_e2e_case_trap "case-27-failure"
cleanup_e2e

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2/test-bin"

cat >"$E2E_DATA_DIR/node-2/test-bin/xdg-open" <<'SH'
#!/bin/sh
test -f "$1"
SH
chmod +x "$E2E_DATA_DIR/node-2/test-bin/xdg-open"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

printf '%s\n' "unsubscribe-purge-open-content" \
    >"$E2E_DATA_DIR/node-1/purge-cycle.txt"
EXPECTED_SHA=$(sha256sum "$E2E_DATA_DIR/node-1/purge-cycle.txt" |
    awk '{print $1}')

UPLOAD_RESULT=$(exec_node node-1 ./proxyma storage upload \
    --name purge-cycle.txt \
    --path /app/data/purge-cycle.txt \
    --storage /app/data)
assert_equals "$UPLOAD_RESULT" \
    "File 'purge-cycle.txt' uploaded successfully to VFS." \
    "Source upload returned an unexpected public result"
exec_node node-1 ./proxyma storage sync --storage /app/data >/dev/null

REMOTE_MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" purge-cycle.txt \
    call_api node-2 GET 8082 manifest)
FILE_HASH=$(printf '%s\n' "$REMOTE_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("purge-cycle.txt", {}).get("hash", ""))
')
assert_equals "$FILE_HASH" "$EXPECTED_SHA" \
    "Remote metadata hash did not match the uploaded content"

SUBSCRIBE_RESULT=$(exec_node node-2 ./proxyma storage subscribe \
    --name purge-cycle.txt --storage /app/data)
assert_equals "$SUBSCRIBE_RESULT" \
    "Subscribed to file 'purge-cycle.txt'. Synchronization triggered." \
    "Initial subscription returned an unexpected public result"
exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null

download_hash_matches() {
    local actual

    actual=$(call_api node-2 GET 8082 "download/$FILE_HASH" |
        sha256sum | awk '{print $1}') || {
        printf '%s\n' "$actual"
        return 1
    }
    printf '%s\n' "$actual"
    [ "$actual" = "$FILE_HASH" ]
}

INITIAL_SHA=$(wait_until "${E2E_DOWNLOAD_TIMEOUT:-60}" \
    "initial subscribed public download" download_hash_matches)
assert_equals "$INITIAL_SHA" "$FILE_HASH" \
    "Initial subscribed download was corrupted"

UNSUBSCRIBE_RESULT=$(exec_node node-2 ./proxyma storage unsubscribe \
    --name purge-cycle.txt --storage /app/data)
assert_equals "$UNSUBSCRIBE_RESULT" \
    "Unsubscribed from file 'purge-cycle.txt'." \
    "Unsubscribe returned an unexpected public result"

PURGE_RESULT=$(exec_node node-2 ./proxyma storage purge \
    --name purge-cycle.txt --storage /app/data)
assert_equals "$PURGE_RESULT" \
    "Physical cache for file 'purge-cycle.txt' purged from disk." \
    "Purge returned an unexpected public result"

PURGED_MANIFEST=$(call_api node-2 GET 8082 manifest)
PURGED_HASH=$(printf '%s\n' "$PURGED_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("purge-cycle.txt", {}).get("hash", ""))
')
assert_equals "$PURGED_HASH" "$FILE_HASH" \
    "Purge removed or changed the public VFS metadata"

PURGED_LIST=$(exec_node node-2 ./proxyma storage list --storage /app/data)
PURGED_STATE=$(printf '%s\n' "$PURGED_LIST" | awk '
$1 == "purge-cycle.txt" {
    print $(NF-3) "|" $(NF-2) "|" $(NF-1) "|" $NF
    exit
}')
assert_equals "$PURGED_STATE" "false|false|Active|$FILE_HASH" \
    "Purged metadata did not remain remote-only and unsubscribed"

blob_status() {
    exec_node node-2 curl \
        --silent --show-error \
        --connect-timeout "${E2E_HTTP_CONNECT_TIMEOUT:-3}" \
        --max-time "${E2E_HTTP_TIMEOUT:-10}" \
        --cacert /app/data/certs/ca.crt \
        --cert /app/data/certs/node-2.crt \
        --key /app/data/certs/node-2.key \
        --output /dev/null \
        --write-out '%{http_code}' \
        "https://localhost:8082/download/$FILE_HASH"
}

PURGED_STATUS=$(blob_status)
assert_equals "$PURGED_STATUS" "404" \
    "Purged blob remained available through the public mTLS endpoint"

RESUBSCRIBE_RESULT=$(exec_node node-2 ./proxyma storage subscribe \
    --name purge-cycle.txt --storage /app/data)
assert_equals "$RESUBSCRIBE_RESULT" \
    "Subscribed to file 'purge-cycle.txt'. Synchronization triggered." \
    "Resubscribe returned an unexpected public result"

open_restores_blob() {
    local output

    output=$(exec_node node-2 env \
        PATH=/app/data/test-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
        ./proxyma storage open \
        --name purge-cycle.txt --storage /app/data 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
}

OPEN_RESULT=$(wait_until "${E2E_DOWNLOAD_TIMEOUT:-60}" \
    "public open to restore the purged blob" open_restores_blob)
assert_equals "$OPEN_RESULT" \
    "File 'purge-cycle.txt' fetched on-demand into cache and opened with system app at: /app/data/preview/purge-cycle.txt" \
    "Public open returned an unexpected recovery result"

RESTORED_SHA=$(download_hash_matches)
assert_equals "$RESTORED_SHA" "$FILE_HASH" \
    "Resubscribe/open restored corrupted content"
RESTORED_CONTENT=$(call_api node-2 GET 8082 "download/$FILE_HASH")
assert_equals "$RESTORED_CONTENT" "unsubscribe-purge-open-content" \
    "Resubscribe/open did not restore the exact original content"

echo -e "${GREEN}✅ Case 27 (VFS unsubscribe, purge, and reopen) passed${NC}"
