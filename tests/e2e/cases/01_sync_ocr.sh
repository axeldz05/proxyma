#!/bin/bash
set -eo pipefail

# E2E project setup
export E2E_PROJECT_NAME="e2e_sync_ocr"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

# Load helpers
SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Basic Sync and OCR...${NC}"

# Initial cleanup
cleanup_e2e
trap cleanup_e2e EXIT

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
    input_path = f"/app/data/{file_hash}"
    output_path = "/tmp/optimized.pdf"
    
    subprocess.run(["ocrmypdf", "--force-ocr", input_path, output_path], check=True, capture_output=True)
    
    upload_cmd = ["curl", "-s", "-X", "POST", "-F", f"file=@{output_path}", "https://localhost:8082/upload", "--cacert", "/app/data/certs/ca.crt", "--cert", "/app/data/certs/node-2.crt", "--key", "/app/data/certs/node-2.key"]
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
run_node node-2 service add ocr \
    --storage "/app/data" \
    --type "script" \
    --exec "python3 /app/data/scripts/ocr_service.py" \
    --desc "OCR my PDF" \
    --param "file_hash:string"

# Bring up Node 1
$COMPOSE_CMD up -d node-1
sleep 2

# Pair and join the cluster
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

# Bring up secondary nodes
$COMPOSE_CMD up -d node-2 node-3
sleep 2

# Upload files to node-1
echo "Hello Proxyma Cluster!" > "$E2E_DATA_DIR/node-1/test_e2e.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.txt" > /dev/null
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.pdf" > /dev/null

# Trigger manual sync on node-3
exec_node node-3 ./proxyma sync > /dev/null

# Verify metadata sync on node-3 VFS
echo "🔍 Checking metadata replication on node-3..."
MAX_RETRIES=10
FILE_FOUND=false
MANIFEST=""
for i in $(seq 1 $MAX_RETRIES); do
    MANIFEST=$(call_api node-3 GET 8083 manifest) || MANIFEST=""
    if echo "$MANIFEST" | grep -q "test_e2e.txt"; then
        FILE_FOUND=true
        break
    fi
    echo "   ... VFS not updated yet (retrying $i/$MAX_RETRIES)..."
    sleep 2
done

if [ "$FILE_FOUND" != "true" ]; then
    echo -e "${RED}❌ Error: File did not reach the VFS of node-3${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Metadata synchronized in VFS correctly.${NC}"

# Get file hash
FILE_HASH=$(echo "$MANIFEST" | grep -o '"test_e2e.txt":{"name":"test_e2e.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

# Verify that the blob was not physically downloaded without being subscribed
if [ -f "$E2E_DATA_DIR/node-3/$FILE_HASH" ]; then
    echo -e "${RED}❌ Logical error: Node 3 downloaded the blob without subscription.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Node 3 ignored physical download (expected behavior).${NC}"

# Subscribe node-3
SUB_RES=$(call_api node-3 POST 8083 "subscribe?name=test_e2e.txt")
if [[ -z "$SUB_RES" || "$SUB_RES" == *"error"* ]]; then
    echo -e "${RED}❌ Error subscribing on node-3${NC}"
    exit 1
fi

# Sync again to force physical download
exec_node node-3 ./proxyma sync > /dev/null

# Verify download and check hash
call_api node-3 GET 8083 "download/$FILE_HASH" > "$E2E_DATA_DIR/node-3/downloaded_test.txt"
if ! diff "$E2E_DATA_DIR/node-1/test_e2e.txt" "$E2E_DATA_DIR/node-3/downloaded_test.txt" > /dev/null; then
    echo -e "${RED}❌ Error: Corrupted data in node-3 download.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Download and cryptographic integrity confirmed.${NC}"

# OCR Test
echo "⚡ Running OCR test..."
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
PDF_HASH=$(echo "$MANIFEST_N1" | grep -o '"test_e2e.pdf":{"name":"test_e2e.pdf","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

call_api node-2 POST 8082 "subscribe?name=test_e2e.pdf" > /dev/null
exec_node node-2 ./proxyma sync > /dev/null

# Send OCR task to node-2
call_api node-2 POST 8082 "services/submit" -d "{\"service\":\"ocr\", \"task_id\":\"ocr_job_1\", \"requester_node_id\":\"host-test\", \"payload\":{\"file_hash\":\"$PDF_HASH\"}}" > /dev/null

# Wait for OCR-processed PDF to appear in the manifest of node-3
echo "⏱️ Waiting for OCR-processed PDF to appear in the manifest of node-3..."
if ! wait_for_condition 30 2 "optimized.pdf" call_api node-3 GET 8083 manifest; then
    echo -e "${RED}❌ Error: OCR failed or file did not propagate.${NC}"
    exit 1
fi

echo -e "${GREEN}🎉 Case 1 (Sync & OCR) completed successfully!${NC}"
