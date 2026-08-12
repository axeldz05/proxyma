#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_stage_name_collision"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Stage name collision (same basename)...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

mkdir -p "$E2E_DATA_DIR/node-1/dirA" "$E2E_DATA_DIR/node-1/dirB"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

# Same basename, different content → distinct CAS hashes / VFS names
echo "content-A-unique" > "$E2E_DATA_DIR/node-1/dirA/out.pdf"
echo "content-B-unique-different" > "$E2E_DATA_DIR/node-1/dirB/out.pdf"

cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/merge.py"
import sys, json, os
payload = json.load(sys.stdin)
a = payload.get("input_a")
b = payload.get("input_b")
out = payload.get("output_path", "/tmp/merged.txt")
if not a or not b:
    print(json.dumps({"error": f"missing inputs: {payload}"}))
    sys.exit(1)
for p in (a, b):
    if not os.path.exists(p):
        print(json.dumps({"error": f"missing file: {p}"}))
        sys.exit(1)
with open(a) as fa, open(b) as fb:
    merged = fa.read().strip() + "|" + fb.read().strip()
parent = os.path.dirname(out)
if parent:
    os.makedirs(parent, exist_ok=True)
with open(out, "w") as f:
    f.write(merged)
print(json.dumps({"status": "success", "output_path": out}))
EOF

bootstrap_node node-1 8081
bootstrap_node node-2 8082

run_node node-2 service add \
    --name "merge_pdfs" \
    --storage "/app/data" \
    --type "script" \
    --exec "python3 /app/data/scripts/merge.py" \
    --desc "Merge two same-basename inputs" \
    --param "input_a:file,input_b:file,output_path?:string"

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" merge_pdfs \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

echo "Running merge with two local out.pdf paths (must stage without collision)..."
RUN_RES=$(exec_node node-1 ./proxyma service run \
    --name merge_pdfs \
    --inputs "input_a=/app/data/dirA/out.pdf,input_b=/app/data/dirB/out.pdf" \
    --storage "/app/data")
echo "run response: $RUN_RES"

OUTPUT_NAME=$(printf '%s\n' "$RUN_RES" | python3 -c '
import json
import sys

response = json.load(sys.stdin)
if response.get("status") != "completed":
    raise SystemExit(f"merge task did not complete: {response!r}")
print(response.get("outputs", {}).get("output_name", ""))
')
assert_not_empty "$OUTPUT_NAME" \
    "merge_pdfs returned no public output name"

MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" "$OUTPUT_NAME" \
    call_api node-1 GET 8081 manifest)
OUTPUT_HASH=$(printf '%s\n' "$MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
name = sys.argv[1]
print(manifest.get(name, {}).get("hash", ""))
' "$OUTPUT_NAME")
assert_not_empty "$OUTPUT_HASH" \
    "Requester manifest exposed no hash for the merged output"

MERGED_CONTENT=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" \
    "content-A-unique|content-B-unique-different" \
    call_api node-1 GET 8081 "download/$OUTPUT_HASH")
assert_equals "$MERGED_CONTENT" \
    "content-A-unique|content-B-unique-different" \
    "Same-basename inputs produced incorrect merged content"

echo -e "${GREEN}✅ Case 20 (stage name collision) passed with exact merged content${NC}"
exit 0
