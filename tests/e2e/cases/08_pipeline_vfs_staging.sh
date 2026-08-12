#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_pipeline_vfs_staging"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Pipeline VFS auto-staging...${NC}"

cleanup_e2e
finish_case() {
    local exit_code=$?
    trap - EXIT
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "case-08-failure"
    fi
    cleanup_e2e
    exit "$exit_code"
}
trap finish_case EXIT

mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/transform.py"
import sys, json, os
payload = json.load(sys.stdin)
inp = payload.get("input_path")
out = payload.get("output_path", "/tmp/staged_out.txt")
if not inp:
    print(json.dumps({"error": f"missing input path: {payload}"}))
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
    --param "input_path:file,output_path?:string"

start_node node-1 8081
join_cluster node-2 node-1 8081
start_node node-2 8082

echo "Waiting for transform to be discoverable through the public CLI..."
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" transform \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

echo "hello-vfs-staging" > "$E2E_DATA_DIR/node-1/source.txt"
UPLOAD_RES=$(exec_node node-1 ./proxyma storage upload \
    --name source.txt \
    --path /app/data/source.txt \
    --storage /app/data)
assert_not_empty "$UPLOAD_RES" "Storage upload returned no public CLI output"

echo "Running transform via service run (VFS stage input + fetch output)..."
RUN_RES=$(exec_node node-1 ./proxyma service run \
    --name transform \
    --inputs "input_path=/app/data/source.txt" \
    --storage /app/data)
assert_not_empty "$RUN_RES" "Transform submission returned no public CLI output"

STATUS_RES=$(wait_for_task_completed "${E2E_TASK_TIMEOUT:-60}" node-1 /app/data)
echo "status: $STATUS_RES"
assert_not_contains "$STATUS_RES" failed "Transform task failed"
assert_contains "$STATUS_RES" completed "Transform task did not complete"

MANIFEST_N1=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" staged_out.txt \
    exec_node node-1 ./proxyma storage list --storage /app/data)
assert_contains "$MANIFEST_N1" staged_out.txt \
    "staged_out.txt was not registered in node-1 VFS"

echo -e "${GREEN}✅ Case 08 (pipeline VFS staging) passed${NC}"
exit 0
