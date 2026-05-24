#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_cgroups"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Resource Limits cgroups...${NC}"

cleanup_on_exit() {
    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}❌ Test failed with exit code $exit_code. Keeping containers for inspection.${NC}"
    else
        cleanup_e2e
    fi
}
trap cleanup_on_exit EXIT

# Initial cleanup
cleanup_e2e

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2" # node-low-spec uses node-2 data volume mapping

# Initialize and bring up Sponsor
bootstrap_node node-1 8081
$COMPOSE_CMD up -d node-1
sleep 2

# Initialize and join node-low-spec
bootstrap_node node-low-spec 8082
join_cluster node-low-spec node-1 8081

# Bring up node-low-spec
$COMPOSE_CMD up -d node-low-spec
sleep 2

# Query telemetry endpoint on node-low-spec
echo "🔍 Querying telemetry endpoint..."
TELEMETRY=$(call_api node-low-spec GET 8082 telemetry)
echo "Telemetry received: $TELEMETRY"

# Verify that it reports real cgroups limits
# We expect cpu_limit = 0.5 and memory_limit = 536870912 (512MB)
CPU_LIMIT=$(echo "$TELEMETRY" | grep -o '"cpu_limit":[^,}]*' | cut -d':' -f2)
MEM_LIMIT=$(echo "$TELEMETRY" | grep -o '"memory_limit":[^,}]*' | cut -d':' -f2)

echo "CPU Limit obtained: $CPU_LIMIT"
echo "Memory Limit obtained: $MEM_LIMIT"

# Verify CPU limit (must be 0.5)
if [ "$CPU_LIMIT" != "0.5" ] && [ "$CPU_LIMIT" != "0.50" ]; then
    echo -e "${RED}❌ Error: Incorrect CPU limit. Expected 0.5, obtained $CPU_LIMIT${NC}"
    exit 1
fi

# Verify Memory limit (must be 536870912)
if [ "$MEM_LIMIT" != "536870912" ]; then
    echo -e "${RED}❌ Error: Incorrect Memory limit. Expected 536870912, obtained $MEM_LIMIT${NC}"
    exit 1
fi

echo -e "${GREEN}✅ cgroups limits reported correctly by telemetry (CPU: 0.5, RAM: 512MB).${NC}"
echo -e "${GREEN}🎉 Case 5 (cgroups limits) completed successfully!${NC}"
