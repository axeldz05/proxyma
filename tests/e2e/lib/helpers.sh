#!/bin/bash

# Standard E2E colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

# Autodetect UID and GID for user mapping in Docker
export HOST_UID=${HOST_UID:-$(id -u)}
export HOST_GID=${HOST_GID:-$(id -g)}

# Verify required variables
if [ -z "$E2E_PROJECT_NAME" ]; then
    echo -e "${RED}Error: E2E_PROJECT_NAME must be defined before loading helpers.sh${NC}"
    exit 1
fi

if [ -z "$E2E_DATA_DIR" ]; then
    echo -e "${RED}Error: E2E_DATA_DIR must be defined before loading helpers.sh${NC}"
    exit 1
fi

# Path to the E2E compose file relative to the helper's location
COMPOSE_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docker-compose.e2e.yml"
COMPOSE_CMD="docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE"

cleanup_e2e() {
    echo -e "${YELLOW}[$E2E_PROJECT_NAME] Cleaning up containers and directories...${NC}"
    $COMPOSE_CMD down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$E2E_DATA_DIR" || true
    # Clean up dynamically created orphan networks if any
    docker network rm "${E2E_PROJECT_NAME}-net-b" >/dev/null 2>&1 || true
}

exec_node() {
    local node_id=$1
    shift
    $COMPOSE_CMD exec -T "$node_id" "$@"
}

run_node() {
    local node_id=$1
    shift
    $COMPOSE_CMD run --rm -T "$node_id" "$@"
}

bootstrap_node() {
    local node_id=$1
    local port=$2
    echo -e "🏗️ Initializing node '$node_id' on port $port..."
    run_node "$node_id" init --id "$node_id" --port "$port" --storage "/app/data" >/dev/null
}

join_cluster() {
    local node_id=$1
    local sponsor_id=$2
    local sponsor_port=$3
    local local_port=$4

    if [ -z "$local_port" ]; then
        if [[ "$node_id" == *"node-1"* ]]; then
            local_port="8081"
        elif [[ "$node_id" == *"node-2"* ]]; then
            local_port="8082"
        elif [[ "$node_id" == *"node-3"* ]]; then
            local_port="8083"
        else
            local_port="8080"
        fi
    fi

    echo -e "🎟️ [$sponsor_id]: Generating invitation token for $node_id..."
    local invite_output
    invite_output=$(exec_node "$sponsor_id" ./proxyma cluster invite)
    local token
    token=$(echo "$invite_output" | tail -n 1 | tr -d '\r\n ')

    if [ -z "$token" ]; then
        echo -e "${RED}❌ Error generating invitation token on $sponsor_id${NC}"
        return 1
    fi

    echo -e "🔗 [$node_id]: Joining the cluster..."
    run_node "$node_id" cluster join --node_id "$node_id" --token "$token" --port "$local_port" >/dev/null
}

call_api() {
    local node_id=$1
    local method=$2
    local port=$3
    local path=$4
    shift 4 # Additional arguments are passed directly to curl

    exec_node "$node_id" curl -s \
        --cacert /app/data/certs/ca.crt \
        --cert "/app/data/certs/$node_id.crt" \
        --key "/app/data/certs/$node_id.key" \
        -X "$method" "$@" "https://localhost:$port/$path"
}

wait_for_condition() {
    local max_retries=$1
    local delay=$2
    local expected=$3
    shift 3
    
    for i in $(seq 1 "$max_retries"); do
        local res
        if res=$("$@") && echo "$res" | grep -q "$expected"; then
            return 0
        fi
        sleep "$delay"
    done
    return 1
}

