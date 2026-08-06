#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_pipeline_vfs_staging"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Pipeline VFS auto-staging...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/transform.py"
import sys, json, os
payload = json.load(sys.stdin)
inp = payload.get("input_path")
out = payload.get("output_path")
if not inp or not out:
    print(json.dumps({"error": f"missing paths: {payload}"}))
    sys.exit(1)
if not os.path.exists(inp):
    print(json.dumps({"error": f"input missing: {inp}"}))
    sys.exit(1)
with open(inp, "r") as f:
    data = f.read()
parent = os.path.dirname(out)
if parent:
    os.makedirs(parent, exist_ok=True)
with open(out, "w") as f:
    f.write("STAGED:" + data)
print(json.dumps({"status": "success", "output_path": out}))
EOF

bootstrap_node node-1 8081
bootstrap_node node-2 8082

run_node node-2 service add \
    --name "transform" \
    --storage "/app/data" \
    --type "script" \
    --exec "python3 /app/data/scripts/transform.py" \
    --desc "Prefix file content" \
    --param "lang?:string"

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
$COMPOSE_CMD up -d node-2
sleep 2

echo "hello-vfs-staging" > "$E2E_DATA_DIR/node-1/source.txt"
exec_node node-1 ./proxyma storage upload --name "source.txt" --path "/app/data/source.txt" --storage "/app/data"

echo "Running transform via service run (VFS stage input + fetch output)..."
exec_node node-1 ./proxyma service run --name transform --inputs "input_path=/app/data/source.txt,output_path=/tmp/staged_out.txt" --storage "/app/data" >/dev/null

STATUS_RES=$(exec_node node-1 ./proxyma service status --storage "/app/data")
echo "status: $STATUS_RES"

if echo "$STATUS_RES" | grep -q "failed"; then
    echo -e "${RED}❌ Transform task failed${NC}"
    exit 1
fi
if ! echo "$STATUS_RES" | grep -q "completed"; then
    echo -e "${RED}❌ Transform task did not complete${NC}"
    exit 1
fi

MANIFEST_N1=$(exec_node node-1 ./proxyma storage list --storage "/app/data")
if ! echo "$MANIFEST_N1" | grep -q "staged_out.txt"; then
    echo -e "${RED}❌ staged_out.txt not registered in node-1 VFS${NC}"
    echo "$MANIFEST_N1"
    exit 1
fi

echo -e "${GREEN}✅ Case 08 (pipeline VFS staging) passed${NC}"
exit 0
