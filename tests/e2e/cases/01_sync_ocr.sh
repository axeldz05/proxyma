#!/bin/bash
set -euo pipefail

# E2E project setup
export E2E_PROJECT_NAME="e2e_sync_ocr"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

# Load helpers
SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Basic Sync and OCR...${NC}"

install_e2e_case_trap "case-01-failure"
cleanup_e2e

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"
mkdir -p "$E2E_DATA_DIR/node-3"

# Generate test PDF
echo "JVBERi0xLjQKMSAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMgMiAwIFIKPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9LaWRzIFszIDAgUl0KL0NvdW50IDEKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1BhcmVudCAyIDAgUgovTWVkaWFCb3ggWzAgMCA1OTUuMjggODQxLjg5XQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA0IDAgUgo+Pgo+PgovQ29udGVudHMgNSAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCi9GMSAyNCBUZgoxMDAgNzAwIFRkCihIZWxsbyBQcm94eW1hIENsdXN0ZXIhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCnhyZWYKMCA2CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAwOSAwMDAwMCBuIAowMDAwMDAwMDU2IDAwMDAwIG4gCjAwMDAwMDAxMTEgMDAwMDAgbiAKMDAwMDAwMDIxMiAwMDAwMCBuIAowMDAwMDAwMjk5IDAwMDAwIG4gCnRyYWlsZXIKPDwKL1NpemUgNgovUm9vdCAxIDAgUgo+PgpzdGFydHhyZWYKMzkzCiUlRU9GCg==" | base64 -d > "$E2E_DATA_DIR/node-1/test_e2e.pdf"

# Generate python script for OCR
cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/ocr_service.py"
import sys, json, subprocess, os
try:
    payload = json.load(sys.stdin)
    file_hash = payload.get("file_hash")
    input_path = "/tmp/input.pdf"
    output_path = "/tmp/optimized.pdf"

    download_cmd = ["curl", "--fail", "--silent", "--show-error", "-o", input_path, f"https://localhost:8082/download/{file_hash}", "--cacert", "/app/data/certs/ca.crt", "--cert", "/app/data/certs/node-2.crt", "--key", "/app/data/certs/node-2.key"]
    subprocess.run(download_cmd, check=True, capture_output=True)
    subprocess.run(["ocrmypdf", "--force-ocr", input_path, output_path], check=True, capture_output=True)

    upload_cmd = ["curl", "--fail", "--silent", "--show-error", "-X", "POST", "-F", f"file=@{output_path}", "https://localhost:8082/upload", "--cacert", "/app/data/certs/ca.crt", "--cert", "/app/data/certs/node-2.crt", "--key", "/app/data/certs/node-2.key"]
    res = subprocess.run(upload_cmd, capture_output=True, text=True, check=True)

    print(json.dumps({"status": "success", "upload_response": res.stdout}))
except Exception as e:
    print(json.dumps({"error": str(e)}))
EOF

# Initialize nodes
bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

# Register OCR service on node-2 using CLI
run_node node-2 service add \
    --name "ocr" \
    --storage "/app/data" \
    --type "script" \
    --exec "python3 /app/data/scripts/ocr_service.py" \
    --desc "OCR my PDF" \
    --param "file_hash:string"

# Bring up Node 1
start_node node-1 8081

# Pair and join the cluster
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

# Bring up secondary nodes
start_nodes node-2 node-3

# Upload files to node-1
echo "Hello Proxyma Cluster!" > "$E2E_DATA_DIR/node-1/test_e2e.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.txt" > /dev/null
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.pdf" > /dev/null

# Trigger manual sync on node-3
exec_node node-3 ./proxyma storage sync > /dev/null

# Verify metadata sync on node-3 VFS
echo "🔍 Checking metadata replication on node-3..."
MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" test_e2e.txt \
    call_api node-3 GET 8083 manifest)
echo -e "${GREEN}✅ Metadata synchronized in VFS correctly.${NC}"

# Get file hash
FILE_HASH=$(echo "$MANIFEST" | grep -o '"test_e2e.txt":{"name":"test_e2e.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)
assert_not_empty "$FILE_HASH" "test_e2e.txt had no public VFS hash"

# Verify through the public download endpoint that unsubscribed metadata did not
# cause a local blob download.
set +e
UNSUBSCRIBED_DOWNLOAD=$(call_api node-3 GET 8083 "download/$FILE_HASH" 2>&1)
UNSUBSCRIBED_RC=$?
set -e
if [ "$UNSUBSCRIBED_RC" -eq 0 ]; then
    fail_assertion "Node 3 exposed file content before subscription" \
        "$UNSUBSCRIBED_DOWNLOAD"
fi
echo -e "${GREEN}✅ Node 3 did not expose an unsubscribed blob.${NC}"

# Subscribe node-3
SUB_RES=$(call_api node-3 POST 8083 "subscribe?name=test_e2e.txt")
if [[ -z "$SUB_RES" || "$SUB_RES" == *"error"* ]]; then
    echo -e "${RED}❌ Error subscribing on node-3${NC}"
    exit 1
fi

# Sync again to force physical download
exec_node node-3 ./proxyma storage sync > /dev/null

# Verify download content through the public mTLS endpoint.
DOWNLOADED_TEXT=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" \
    "Hello Proxyma Cluster!" call_api node-3 GET 8083 "download/$FILE_HASH")
assert_equals "$DOWNLOADED_TEXT" "Hello Proxyma Cluster!" \
    "Node 3 returned corrupted downloaded content"
echo -e "${GREEN}✅ Download and cryptographic integrity confirmed.${NC}"

# OCR Test
echo "⚡ Running OCR test..."
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
PDF_HASH=$(echo "$MANIFEST_N1" | grep -o '"test_e2e.pdf":{"name":"test_e2e.pdf","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

call_api node-2 POST 8082 "subscribe?name=test_e2e.pdf" > /dev/null
exec_node node-2 ./proxyma storage sync > /dev/null

# Execute through the public requester CLI. This keeps the authenticated
# requester identity consistent with the task envelope instead of posting a
# forged requester_node_id directly to the provider.
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" ocr \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null
wait_for_peer "${E2E_PEER_TIMEOUT:-45}" node-2 node-3
OCR_RUN=$(exec_node node-1 ./proxyma service run \
    --name ocr \
    --payload "{\"file_hash\":\"$PDF_HASH\"}" \
    --storage /app/data)
assert_contains "$OCR_RUN" '"status": "completed"' \
    "Distributed OCR task did not complete"

# Wait for OCR-processed PDF to appear in the manifest of node-3.
echo "⏱️ Waiting for OCR-processed PDF to appear in the manifest of node-3..."
OPTIMIZED_MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-60}" optimized.pdf \
    call_api node-3 GET 8083 manifest)
OPTIMIZED_HASH=$(printf '%s\n' "$OPTIMIZED_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
print(manifest.get("optimized.pdf", {}).get("hash", ""))
')
assert_not_empty "$OPTIMIZED_HASH" \
    "Node 3 manifest exposed no hash for optimized.pdf"

OPTIMIZED_SUBSCRIPTION=$(call_api node-3 POST 8083 "subscribe?name=optimized.pdf")
assert_contains "$OPTIMIZED_SUBSCRIPTION" "Subscribed to optimized.pdf" \
    "Node 3 did not acknowledge the optimized.pdf subscription"

optimized_download_ready() {
    local status actual_hash

    exec_node node-3 ./proxyma storage sync --storage /app/data >/dev/null 2>&1 || true
    status=$(call_api node-3 GET 8083 "download/$OPTIMIZED_HASH" \
        --output /tmp/node-3-optimized.pdf \
        --write-out '%{http_code}' 2>&1) || {
        printf '%s\n' "$status"
        return 1
    }
    actual_hash=$(exec_node node-3 sha256sum /tmp/node-3-optimized.pdf |
        awk '{print $1}')
    printf '%s\n' "$actual_hash"
    [ "$status" = "200" ] && [ "$actual_hash" = "$OPTIMIZED_HASH" ]
}

OPTIMIZED_SHA=$(wait_until "${E2E_DOWNLOAD_TIMEOUT:-60}" \
    "node-3 public download of optimized.pdf" optimized_download_ready)
assert_equals "$OPTIMIZED_SHA" "$OPTIMIZED_HASH" \
    "Downloaded optimized.pdf SHA-256 did not match its manifest hash"

OPTIMIZED_PROPERTIES=$(exec_node node-3 python3 -c '
import pathlib
import sys

content = pathlib.Path(sys.argv[1]).read_bytes()
print("{}|{}".format(len(content), content[:5].decode("ascii", errors="replace")))
' /tmp/node-3-optimized.pdf)
OPTIMIZED_SIZE=${OPTIMIZED_PROPERTIES%%|*}
OPTIMIZED_MAGIC=${OPTIMIZED_PROPERTIES#*|}
if ! [[ "$OPTIMIZED_SIZE" =~ ^[1-9][0-9]*$ ]]; then
    fail_assertion "Downloaded optimized.pdf was empty" "$OPTIMIZED_PROPERTIES"
fi
assert_equals "$OPTIMIZED_MAGIC" "%PDF-" \
    "Downloaded optimized.pdf did not have PDF magic"

echo -e "${GREEN}🎉 Case 1 (Sync & OCR) completed successfully!${NC}"
