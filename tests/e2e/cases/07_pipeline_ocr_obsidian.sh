#!/bin/bash
export E2E_PROJECT_NAME="e2e_pipeline_ocr_obsidian"
export E2E_DATA_DIR="/tmp/proxyma-e2e-$E2E_PROJECT_NAME"
source "$(dirname "${BASH_SOURCE[0]}")/../lib/helpers.sh"

trap cleanup_e2e EXIT

# Prepare scripts directory on nodes
mkdir -p "$E2E_DATA_DIR/node-1/scripts"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

# Copy service scripts to node directories so they are mounted in Docker
cp /home/drusila/Projects/proxyma-services/ocr_service.py "$E2E_DATA_DIR/node-2/scripts/ocr_service.py"
cp /home/drusila/Projects/proxyma-services/extract_service.py "$E2E_DATA_DIR/node-2/scripts/extract_service.py"
cp /home/drusila/Projects/proxyma-services/obsidian_service.py "$E2E_DATA_DIR/node-1/scripts/obsidian_service.py"

# Write service JSON definitions (type, exec, schema)
cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/ocr_service.json"
{
    "type": "script",
    "exec": "python3 /app/data/scripts/ocr_service.py",
    "schema": {
        "name": "ocr",
        "description": "OCR my PDF or Image",
        "parameters": {
            "input_path": {"type": "string", "required": true},
            "lang": {"type": "string", "required": false},
            "force_ocr": {"type": "bool", "required": false}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "output_path": {"type": "string"}
        }
    }
}
EOF

cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/extract_service.json"
{
    "type": "script",
    "exec": "python3 /app/data/scripts/extract_service.py",
    "schema": {
        "name": "text/extract",
        "description": "Extract text from PDF or Image",
        "parameters": {
            "input_path": {"type": "string", "required": true}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "text": {"type": "string"}
        }
    }
}
EOF

cat << 'EOF' > "$E2E_DATA_DIR/node-1/scripts/obsidian_service.json"
{
    "type": "script",
    "exec": "python3 /app/data/scripts/obsidian_service.py",
    "schema": {
        "name": "obsidian/save",
        "description": "Save text to Obsidian note",
        "parameters": {
            "text": {"type": "string", "required": true},
            "vault_path": {"type": "string", "required": true},
            "note_name": {"type": "string", "required": false}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "note_path": {"type": "string"}
        }
    }
}
EOF

# Copy pipeline schema file
cp /home/drusila/Projects/proxyma-services/ocr_obsidian_pipeline.json "$E2E_DATA_DIR/node-1/scripts/ocr_obsidian_pipeline.json"

# Create a sample PDF file to OCR on node-1
echo "JVBERi0xLjQKMSAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMgMiAwIFIKPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9LaWRzIFszIDAgUl0KL0NvdW50IDEKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1BhcmVudCAyIDAgUgovTWVkaWFCb3ggWzAgMCA1OTUuMjggODQxLjg5XQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA0IDAgUgo+Pgo+PgovQ29udGVudHMgNSAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCi9GMSAyNCBUZgoxMDAgNzAwIFRkCihIZWxsbyBQcm94eW1hIENsdXN0ZXIhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCnhyZWYKMCA2CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAwOSAwMDAwMCBuIAowMDAwMDAwMDU2IDAwMDAwIG4gCjAwMDAwMDAxMTEgMDAwMDAgbiAKMDAwMDAwMDIxMiAwMDAwMCBuIAowMDAwMDAwMDI5OSAwMDAwMCBuIAp0cmFpbGVyCjw8Ci9TaXplIDYKL1Jvb3QgMSAwIFIKPj4Kc3RhcnR4cmVmCjM5MwpfX0VPRl9fCg==" | base64 -d > "$E2E_DATA_DIR/node-1/test_pipeline.pdf"

# Initialize nodes
bootstrap_node node-1 8081
bootstrap_node node-2 8082

# Start Compose containers
$COMPOSE_CMD up -d node-1
sleep 2

# Pair nodes
join_cluster node-2 node-1 8081

$COMPOSE_CMD up -d node-2
sleep 2

# Install dependencies in containers
echo "Installing python packages in containers..."
exec_node node-1 env HOME=/tmp pip3 install pypdf pytesseract pillow --disable-pip-version-check
exec_node node-2 env HOME=/tmp pip3 install pypdf pytesseract pillow --disable-pip-version-check

# Register services using JSON file parameters
echo "Registering services..."
run_node node-2 service add --name "/app/data/scripts/ocr_service.json" --storage "/app/data"
run_node node-2 service add --name "/app/data/scripts/extract_service.json" --storage "/app/data"
run_node node-1 service add --name "/app/data/scripts/obsidian_service.json" --storage "/app/data"

# Wait for gossip to sync services
echo "Waiting for service discovery to propagate..."
sleep 4

# Register the pipeline on Node 1
echo "Registering pipeline..."
exec_node node-1 ./proxyma service add_pipeline \
    --id "ocr-obsidian-pipeline" \
    --schema-file "/app/data/scripts/ocr_obsidian_pipeline.json" \
    --storage "/app/data"

# Run the pipeline using run_file
echo "Running the pipeline..."
RUN_RES=$(exec_node node-1 ./proxyma service run_file \
    --service "ocr-obsidian-pipeline" \
    --input "/app/data/test_pipeline.pdf" \
    --output "ocr_output.pdf" \
    --param '{"vault_path_a": "/app/data/obsidian_vault", "note_name": "extracted_note"}' \
    --storage "/app/data")
echo "Run Result: $RUN_RES"

# Wait a moment for pipeline task to finish
sleep 8

# Check status of the pipeline task
echo "Checking task status..."
STATUS_RES=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "Status response: $STATUS_RES"

if echo "$STATUS_RES" | grep -q "failed"; then
    echo -e "${RED}❌ Pipeline execution failed!${NC}"
    exit 1
fi

if ! echo "$STATUS_RES" | grep -q "completed"; then
    echo -e "${RED}❌ Pipeline task did not complete!${NC}"
    exit 1
fi

# Verify the Obsidian note was created and contains correct text on Node 1
NOTE_FILE="$E2E_DATA_DIR/node-1/obsidian_vault/extracted_note.md"
if [ ! -f "$NOTE_FILE" ]; then
    echo -e "${RED}❌ Obsidian note was not created at $NOTE_FILE!${NC}"
    exit 1
fi

NOTE_CONTENT=$(cat "$NOTE_FILE")
echo "Note content: $NOTE_CONTENT"
if ! echo "$NOTE_CONTENT" | grep -q "Hello Proxyma Cluster"; then
    echo -e "${RED}❌ Note content does not contain OCR extracted text!${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Pipeline created note successfully on Node 1.${NC}"

# Run the pipeline a second time to test the appending behavior
echo "Running pipeline a second time (testing append)..."
RUN_RES_2=$(exec_node node-1 ./proxyma service run_file \
    --service "ocr-obsidian-pipeline" \
    --input "/app/data/test_pipeline.pdf" \
    --output "ocr_output2.pdf" \
    --param '{"vault_path_a": "/app/data/obsidian_vault", "note_name": "extracted_note"}' \
    --storage "/app/data")
sleep 8

STATUS_RES_2=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "Status response 2: $STATUS_RES_2"

NOTE_CONTENT_2=$(cat "$NOTE_FILE")
if ! echo "$NOTE_CONTENT_2" | grep -q "OCR Append -"; then
    echo -e "${RED}❌ Note content was not appended with a timestamp header!${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Pipeline appended content successfully to existing note.${NC}"
exit 0
