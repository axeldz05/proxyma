#!/bin/bash
set -euo pipefail

export E2E_PROJECT_NAME="e2e_pipeline_validation_cli"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Pipeline validation through CLI...${NC}"

install_e2e_case_trap "case-16-failure"
cleanup_e2e

mkdir -p "$E2E_DATA_DIR/node-1/schemas"

cat >"$E2E_DATA_DIR/node-1/schemas/source.json" <<'JSON'
{
  "name": "validation.source",
  "type": "script",
  "description": "Validation-only source",
  "parameters": {
    "input": {"type": "string", "required": false}
  },
  "outputs": {
    "text": {"type": "string"}
  }
}
JSON

cat >"$E2E_DATA_DIR/node-1/schemas/consumer.json" <<'JSON'
{
  "name": "validation.consumer",
  "type": "script",
  "description": "Validation-only consumer",
  "parameters": {
    "text": {"type": "string", "required": false},
    "count": {"type": "int", "required": false}
  },
  "outputs": {
    "result": {"type": "string"}
  }
}
JSON

cat >"$E2E_DATA_DIR/node-1/schemas/cycle.json" <<'JSON'
{
  "id": "validation-cycle",
  "version": 1,
  "steps": [
    {"id": "source", "service": "validation.source"},
    {"id": "consumer", "service": "validation.consumer"}
  ],
  "connections": [
    {
      "from_step": "source",
      "from_port": "text",
      "to_step": "consumer",
      "to_port": "text"
    },
    {
      "from_step": "consumer",
      "from_port": "result",
      "to_step": "source",
      "to_port": "input"
    }
  ]
}
JSON

cat >"$E2E_DATA_DIR/node-1/schemas/mismatch.json" <<'JSON'
{
  "id": "validation-mismatch",
  "version": 1,
  "steps": [
    {"id": "source", "service": "validation.source"},
    {"id": "consumer", "service": "validation.consumer"}
  ],
  "connections": [
    {
      "from_step": "source",
      "from_port": "text",
      "to_step": "consumer",
      "to_port": "count"
    }
  ]
}
JSON

cat >"$E2E_DATA_DIR/node-1/schemas/valid.json" <<'JSON'
{
  "id": "validation-valid",
  "version": 1,
  "steps": [
    {"id": "source", "service": "validation.source"},
    {"id": "consumer", "service": "validation.consumer"}
  ],
  "connections": [
    {
      "from_step": "source",
      "from_port": "text",
      "to_step": "consumer",
      "to_port": "text"
    }
  ]
}
JSON

bootstrap_node node-1 8081
run_node node-1 service add \
    --name validation.source \
    --type script \
    --exec "printf '{}'" \
    --schema-file /app/data/schemas/source.json \
    --storage /app/data >/dev/null
run_node node-1 service add \
    --name validation.consumer \
    --type script \
    --exec "printf '{}'" \
    --schema-file /app/data/schemas/consumer.json \
    --storage /app/data >/dev/null
start_node node-1 8081

expect_pipeline_rejection() {
    local pipeline_id=$1
    local schema_file=$2
    local expected=$3
    local output exit_code

    set +e
    output=$(exec_node node-1 ./proxyma service add_pipeline \
        --id "$pipeline_id" \
        --schema-file "$schema_file" \
        --storage /app/data 2>&1)
    exit_code=$?
    set -e

    if [ "$exit_code" -eq 0 ]; then
        fail_assertion "CLI accepted invalid pipeline '$pipeline_id'" "$output"
    fi
    assert_contains "$output" "$expected" \
        "CLI rejected '$pipeline_id' without the expected validation reason"
}

expect_pipeline_rejection \
    validation-cycle /app/data/schemas/cycle.json "contains a cycle"
expect_pipeline_rejection \
    validation-mismatch /app/data/schemas/mismatch.json "type mismatch"

VALID_RESULT=$(exec_node node-1 ./proxyma service add_pipeline \
    --id validation-valid \
    --schema-file /app/data/schemas/valid.json \
    --storage /app/data)
assert_contains "$VALID_RESULT" "Pipeline added successfully" \
    "CLI did not accept the valid pipeline schema"

PIPELINES=$(exec_node node-1 ./proxyma service list_pipelines --storage /app/data)
assert_contains "$PIPELINES" validation-valid \
    "The accepted pipeline is absent from the public CLI"
assert_not_contains "$PIPELINES" validation-cycle \
    "The cycle pipeline was persisted despite CLI rejection"
assert_not_contains "$PIPELINES" validation-mismatch \
    "The type-mismatched pipeline was persisted despite CLI rejection"

echo -e "${GREEN}✅ Case 16 (pipeline validation CLI) passed${NC}"
