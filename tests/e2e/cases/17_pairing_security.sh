#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_pairing_security"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Pairing and mTLS security...${NC}"

cleanup_e2e
finish_case() {
    local exit_code=$?
    trap - EXIT
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "case-17-failure"
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

NO_CERT_RESPONSE=$(exec_node node-1 curl \
    --silent --show-error \
    --connect-timeout "${E2E_HTTP_CONNECT_TIMEOUT:-3}" \
    --max-time "${E2E_HTTP_TIMEOUT:-10}" \
    --cacert /app/data/certs/ca.crt \
    --write-out $'\n%{http_code}' \
    https://localhost:8081/manifest)
NO_CERT_STATUS=${NO_CERT_RESPONSE##*$'\n'}
assert_equals "$NO_CERT_STATUS" "403" \
    "Protected manifest endpoint did not reject a client without a certificate"

INVITE_OUTPUT=$(exec_node node-1 ./proxyma cluster invite --storage /app/data)
INVITE_TOKEN=$(printf '%s\n' "$INVITE_OUTPUT" | tail -n 1 | tr -d '\r\n ')
assert_not_empty "$INVITE_TOKEN" "Sponsor CLI returned an empty invite"

run_node node-2 cluster join \
    --node_id node-2 \
    --token "$INVITE_TOKEN" \
    --port 8082 \
    --storage /app/data >/dev/null

set +e
REPLAY_OUTPUT=$(run_node node-3 cluster join \
    --node_id node-3 \
    --token "$INVITE_TOKEN" \
    --port 8083 \
    --storage /app/data 2>&1)
REPLAY_STATUS=$?
set -e

if [ "$REPLAY_STATUS" -eq 0 ]; then
    fail_assertion "A consumed invite was accepted by the public join CLI"
fi
assert_contains "$REPLAY_OUTPUT" "Status 401" \
    "Invite replay was rejected without the expected unauthorized response"

start_node node-2 8082
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-2 \
    exec_node node-1 ./proxyma peers list --storage /app/data >/dev/null

printf '%s\n' "pairing-security-ok" >"$E2E_DATA_DIR/node-2/pairing.txt"
UPLOAD_RESULT=$(exec_node node-2 ./proxyma storage upload \
    --name pairing.txt \
    --path /app/data/pairing.txt \
    --storage /app/data)
assert_contains "$UPLOAD_RESULT" "uploaded successfully" \
    "The enrolled node could not upload through its public CLI"
exec_node node-2 ./proxyma storage sync --storage /app/data >/dev/null

wait_for_output "${E2E_VFS_TIMEOUT:-45}" pairing.txt \
    call_api node-1 GET 8081 manifest >/dev/null

echo -e "${GREEN}✅ Case 17 (pairing security) passed${NC}"
