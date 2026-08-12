#!/bin/bash
set -eo pipefail

# E2E project setup
export E2E_PROJECT_NAME="e2e_generic_file_ocr"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

# Load helpers
SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Generic File OCR Pipeline...${NC}"

# Initial cleanup
cleanup_e2e
trap cleanup_e2e EXIT

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

# Generate test PDF
echo "JVBERi0xLjQKMSAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMgMiAwIFIKPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9LaWRzIFszIDAgUl0KL0NvdW50IDEKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1BhcmVudCAyIDAgUgovTWVkaWFCb3ggWzAgMCA1OTUuMjggODQxLjg5XQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA0IDAgUgo+Pgo+PgovQ29udGVudHMgNSAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCi9GMSAyNCBUZgoxMDAgNzAwIFRkCihIZWxsbyBQcm94eW1hIENsdXN0ZXIhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCnhyZWYKMCA2CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAwOSAwMDAwMCBuIAowMDAwMDAwMDU2IDAwMDAwIG4gCjAwMDAwMDAxMTEgMDAwMDAgbiAKMDAwMDAwMDIxMiAwMDAwMCBuIAowMDAwMDAwMDI5OSAwMDAwMCBuIAp0cmFpbGVyCjw8Ci9TaXplIDYKL1Jvb3QgMSAwIFIKPj4Kc3RhcnR4cmVmCjM5MwpfX0VPRl9fCg==" | base64 -d > "$E2E_DATA_DIR/node-1/test_e2e.pdf"

# Generate python script for generic file OCR
cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/ocr_service.py"
import sys, json, subprocess, os
try:
    payload = json.load(sys.stdin)
    input_path = payload.get("input_path")
    output_name = os.path.basename(payload.get("output_name", "optimized.pdf"))
    output_path = os.path.join("/tmp", output_name)
    
    if not input_path:
        print(json.dumps({"error": f"Missing input_path in payload: {json.dumps(payload)}"}))
        sys.exit(1)
        
    if not os.path.exists(input_path):
        print(json.dumps({"error": f"Input file not found: {input_path}"}))
        sys.exit(1)
        
    # Execute ocrmypdf
    res = subprocess.run(["ocrmypdf", "--force-ocr", input_path, output_path], capture_output=True, text=True)
    if res.returncode == 0:
        print(json.dumps({
            "status": "success",
            "message": "OCR completed successfully",
            "output_path": output_path
        }))
    else:
        print(json.dumps({
            "error": f"ocrmypdf failed with code {res.returncode}",
            "stdout": res.stdout,
            "stderr": res.stderr
        }))
        sys.exit(res.returncode)
except Exception as e:
    print(json.dumps({"error": str(e)}))
    sys.exit(1)
EOF

# Initialize nodes
bootstrap_node node-1 8081
bootstrap_node node-2 8082

# Register OCR service on node-2 using CLI
run_node node-2 service add \
    --name "ocr" \
    --storage "/app/data" \
    --type "script" \
    --exec "python3 /app/data/scripts/ocr_service.py" \
    --desc "OCR my PDF" \
    --param "input_path:file,output_name?:string,lang?:string,force_ocr?:bool"

# Bring up Node 1
start_node node-1 8081

# Pair and join the cluster
join_cluster node-2 node-1 8081

# Bring up secondary node
start_node node-2 8082

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" ocr \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

# Upload files to node-1 VFS
echo "Uploading test_e2e.pdf to node-1..."
exec_node node-1 ./proxyma storage upload --name "test_e2e.pdf" --path "/app/data/test_e2e.pdf" --storage "/app/data"

# Run generic file-processing task from node-1
echo "Running OCR service from node-1..."
RUN_RES=$(exec_node node-1 ./proxyma service run --name ocr \
    --inputs "input_path=/app/data/test_e2e.pdf,output_name=optimized.pdf" \
    --storage "/app/data")
echo "Result from run_file: $RUN_RES"

# Check status using service status
echo "Checking task status..."
STATUS_RES=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "Status response: $STATUS_RES"

if echo "$STATUS_RES" | grep -q "failed"; then
    echo -e "${RED}❌ Generic File OCR task failed!${NC}"
    exit 1
fi

if ! echo "$STATUS_RES" | grep -q "completed"; then
    echo -e "${RED}❌ Generic File OCR task did not complete!${NC}"
    exit 1
fi

# Verify that the output optimized.pdf is present in node-1 VFS
MANIFEST_N1=$(exec_node node-1 ./proxyma storage list --storage "/app/data")
if ! echo "$MANIFEST_N1" | grep -q "optimized.pdf"; then
    echo -e "${RED}❌ Output optimized.pdf not registered in node-1 VFS registry!${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Output file registered in requester VFS registry.${NC}"

# Second run with explicit default-style output name
echo "Running OCR service from node-1 with output_test_e2e.pdf name..."
RUN_RES_EMPTY=$(exec_node node-1 ./proxyma service run --name ocr \
    --inputs "input_path=/app/data/test_e2e.pdf,output_name=output_test_e2e.pdf" \
    --storage "/app/data")
echo "Result from second run: $RUN_RES_EMPTY"

# Verify that output_test_e2e.pdf is registered in VFS
MANIFEST_N1_EMPTY=$(exec_node node-1 ./proxyma storage list --storage "/app/data")
if ! echo "$MANIFEST_N1_EMPTY" | grep -q "output_test_e2e.pdf"; then
    echo -e "${RED}❌ output_test_e2e.pdf not registered in node-1 VFS registry!${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Default-style output file (output_test_e2e.pdf) registered successfully.${NC}"
echo -e "${GREEN}🎉 Case 6 (Generic File OCR Pipeline) completed successfully!${NC}"
exit 0
