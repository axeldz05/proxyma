#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_pipeline_multinode_dag"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Three-node pipeline DAG...${NC}"

cleanup_e2e
finish_case() {
    local exit_code=$?
    trap - EXIT
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "case-15-failure"
    fi
    cleanup_e2e
    exit "$exit_code"
}
trap finish_case EXIT

mkdir -p \
    "$E2E_DATA_DIR/node-1" \
    "$E2E_DATA_DIR/node-2/scripts" \
    "$E2E_DATA_DIR/node-3/scripts"

cat >"$E2E_DATA_DIR/node-2/scripts/prefix.py" <<'PY'
import json
import os
import sys

payload = json.load(sys.stdin)
input_path = payload.get("input_path")
if not input_path or not os.path.isfile(input_path):
    raise SystemExit("input_path is not an available file")

with open(input_path, encoding="utf-8") as source:
    value = source.read().rstrip("\n")

output_path = "/tmp/proxyma-dag-prefixed.txt"
with open(output_path, "w", encoding="utf-8") as output:
    output.write(f"prefix[{value}]")

print(json.dumps({"status": "ok", "output_path": output_path}))
PY

cat >"$E2E_DATA_DIR/node-3/scripts/suffix.py" <<'PY'
import json
import os
import sys

payload = json.load(sys.stdin)
input_path = payload.get("input_path")
if not input_path or not os.path.isfile(input_path):
    raise SystemExit("input_path is not an available file")

with open(input_path, encoding="utf-8") as source:
    value = source.read()

output_path = "/tmp/proxyma-dag-final.txt"
with open(output_path, "w", encoding="utf-8") as output:
    output.write(f"{value}|suffix")

print(json.dumps({"status": "ok", "output_path": output_path}))
PY

cat >"$E2E_DATA_DIR/node-2/scripts/prefix_schema.json" <<'JSON'
{
  "name": "dag.prefix",
  "type": "script",
  "description": "Deterministically prefix a text file",
  "parameters": {
    "input_path": {"type": "file", "required": true}
  },
  "outputs": {
    "status": {"type": "string"},
    "output_path": {"type": "file"}
  }
}
JSON

cat >"$E2E_DATA_DIR/node-3/scripts/suffix_schema.json" <<'JSON'
{
  "name": "dag.suffix",
  "type": "script",
  "description": "Deterministically suffix a text file",
  "parameters": {
    "input_path": {"type": "file", "required": true}
  },
  "outputs": {
    "status": {"type": "string"},
    "output_path": {"type": "file"}
  }
}
JSON

cat >"$E2E_DATA_DIR/node-1/dag.json" <<'JSON'
{
  "id": "dag-two-provider",
  "version": 1,
  "steps": [
    {"id": "prefix", "service": "dag.prefix", "target_node_id": "node-2"},
    {"id": "suffix", "service": "dag.suffix", "target_node_id": "node-3"}
  ],
  "connections": [
    {
      "from_step": "$initial",
      "from_port": "source",
      "to_step": "prefix",
      "to_port": "input_path"
    },
    {
      "from_step": "prefix",
      "from_port": "output_path",
      "to_step": "suffix",
      "to_port": "input_path"
    }
  ]
}
JSON

printf '%s\n' "dag-contract" >"$E2E_DATA_DIR/node-1/dag-input.txt"

bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

run_node node-2 service add \
    --name dag.prefix \
    --type script \
    --exec "python3 /app/data/scripts/prefix.py" \
    --schema-file /app/data/scripts/prefix_schema.json \
    --storage /app/data >/dev/null
run_node node-3 service add \
    --name dag.suffix \
    --type script \
    --exec "python3 /app/data/scripts/suffix.py" \
    --schema-file /app/data/scripts/suffix_schema.json \
    --storage /app/data >/dev/null

start_node node-1 8081
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081
start_nodes node-2 node-3

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-3 \
    exec_node node-2 ./proxyma peers list --storage /app/data >/dev/null
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" dag.prefix \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" dag.suffix \
    exec_node node-1 ./proxyma service discover --storage /app/data >/dev/null

ADD_RESULT=$(exec_node node-1 ./proxyma service add_pipeline \
    --id dag-two-provider \
    --schema-file /app/data/dag.json \
    --storage /app/data)
assert_contains "$ADD_RESULT" "Pipeline added successfully" \
    "The valid two-step pipeline was not accepted"

wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" dag-two-provider \
    exec_node node-2 ./proxyma service get_pipeline \
        --id dag-two-provider --storage /app/data >/dev/null
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" dag-two-provider \
    exec_node node-3 ./proxyma service get_pipeline \
        --id dag-two-provider --storage /app/data >/dev/null

# The input remains a plain requester-local path. Dispatch must stage it
# automatically before the first step runs on node-2.
RUN_RESULT=$(exec_node node-1 ./proxyma service run_pipeline \
    --id dag-two-provider \
    --payload '{"source":"/app/data/dag-input.txt"}' \
    --storage /app/data)
assert_contains "$RUN_RESULT" '"status": "completed"' \
    "The distributed pipeline did not complete"

OUTPUT_NAME=$(printf '%s\n' "$RUN_RESULT" | python3 -c '
import json, sys
response = json.load(sys.stdin)
print(response.get("outputs", {}).get("output_name", ""))
')
assert_not_empty "$OUTPUT_NAME" \
    "The requester did not receive a public output name"

STAGED_LIST=$(exec_node node-1 ./proxyma storage list --storage /app/data)
assert_contains "$STAGED_LIST" "dag-input.txt" \
    "The requester CLI did not expose the automatically staged input"

FINAL_MANIFEST=$(wait_for_output "${E2E_VFS_TIMEOUT:-45}" "$OUTPUT_NAME" \
    call_api node-1 GET 8081 manifest)
OUTPUT_HASH=$(printf '%s\n' "$FINAL_MANIFEST" | python3 -c '
import json
import sys

manifest = json.load(sys.stdin)
name = sys.argv[1]
print(manifest.get(name, {}).get("hash", ""))
' "$OUTPUT_NAME")
assert_not_empty "$OUTPUT_HASH" \
    "The requester manifest exposed no hash for the final pipeline output"

FINAL_CONTENT=$(call_api node-1 GET 8081 "download/$OUTPUT_HASH")
assert_equals "$FINAL_CONTENT" "prefix[dag-contract]|suffix" \
    "The requester downloaded unexpected final pipeline output"

echo -e "${GREEN}✅ Case 15 (three-node pipeline DAG) passed${NC}"
