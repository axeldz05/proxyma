#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_sponsor_outage_peer_mesh"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}Starting test case: peer mesh remains functional without sponsor...${NC}"

install_e2e_case_trap "case-29-failure"
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

# Both peers must know the full mesh before the bootstrap/sponsor leaves.
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-2 node-3
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-3 node-2
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-3 \
    call_peer_api node-2 node-3 GET 8083 telemetry >/dev/null

echo "Stopping sponsor while the two enrolled peers remain online..."
e2e_compose stop node-1 >/dev/null

FILE_NAME="sponsor_outage.txt"
printf '%s\n' "peer mesh survives sponsor outage" >"$E2E_DATA_DIR/node-2/$FILE_NAME"
SOURCE_SHA256=$(sha256sum "$E2E_DATA_DIR/node-2/$FILE_NAME" | awk '{print $1}')

call_api node-2 POST 8082 upload \
    -F "file=@/app/data/$FILE_NAME" >/dev/null
call_api node-3 POST 8083 "subscribe?name=$FILE_NAME" >/dev/null

peer_mesh_download_ready() {
    local manifest actual_sha

    # A stopped sponsor may make each sync report a partial peer failure. The
    # observable contract is that the surviving peer route still converges.
    exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null 2>&1 || true
    exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null 2>&1 || true

    manifest=$(call_api node-3 GET 8083 manifest 2>&1) || {
        printf '%s\n' "$manifest"
        return 1
    }
    if [[ "$manifest" != *"$FILE_NAME"* || "$manifest" != *"$SOURCE_SHA256"* ]]; then
        printf '%s\n' "$manifest"
        return 1
    fi

    actual_sha=$(call_api node-3 GET 8083 "download/$SOURCE_SHA256" |
        sha256sum | awk '{print $1}') || return 1
    printf '%s\n' "$actual_sha"
    [ "$actual_sha" = "$SOURCE_SHA256" ]
}

RECOVERED_SHA256=$(wait_until "${E2E_VFS_TIMEOUT:-60}" \
    "exact peer-to-peer download while sponsor is stopped" \
    peer_mesh_download_ready)
assert_equals "$RECOVERED_SHA256" "$SOURCE_SHA256" \
    "Surviving peers did not exchange the exact blob without the sponsor"

echo -e "${GREEN}Case 29 (peer mesh without sponsor) passed${NC}"
