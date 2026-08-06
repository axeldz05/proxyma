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

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
$COMPOSE_CMD up -d node-2
sleep 2

echo "Running merge with two local out.pdf paths (must stage without collision)..."
exec_node node-1 ./proxyma service run \
    --name merge_pdfs \
    --inputs "input_a=/app/data/dirA/out.pdf,input_b=/app/data/dirB/out.pdf,output_path=/tmp/merged_out.txt" \
    --storage "/app/data" >/dev/null

STATUS_RES=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "status: $STATUS_RES"
if echo "$STATUS_RES" | grep -q "failed"; then
    echo -e "${RED}❌ merge_pdfs task failed${NC}"
    exit 1
fi
if ! echo "$STATUS_RES" | grep -q "completed"; then
    echo -e "${RED}❌ merge_pdfs task did not complete${NC}"
    exit 1
fi

MANIFEST=$(exec_node node-1 ./proxyma storage list --storage "/app/data")
echo "manifest: $MANIFEST"

# Expect stage/<hash>/out.pdf for each distinct content
STAGE_LINES=$(echo "$MANIFEST" | grep -c 'stage/.*/out.pdf' || true)
if [ "$STAGE_LINES" -lt 2 ]; then
    echo -e "${RED}❌ Expected ≥2 stage/*/out.pdf entries, got $STAGE_LINES${NC}"
    exit 1
fi

# Distinct hashes (two different stage/<hash>/ prefixes)
HASHES=$(echo "$MANIFEST" | grep -oE 'stage/[a-f0-9]+/out\.pdf' | sed 's|stage/\([a-f0-9]*\)/out.pdf|\1|' | sort -u)
HASH_COUNT=$(echo "$HASHES" | grep -c . || true)
if [ "$HASH_COUNT" -lt 2 ]; then
    echo -e "${RED}❌ Expected two distinct stage hashes for out.pdf, got:${NC}"
    echo "$HASHES"
    exit 1
fi

echo -e "${GREEN}✅ Case 20 (stage name collision) passed — $HASH_COUNT distinct stage hashes${NC}"
exit 0
