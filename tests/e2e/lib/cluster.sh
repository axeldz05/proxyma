#!/bin/bash

# Cluster lifecycle and public CLI/HTTP access.

GREEN=${GREEN:-'\033[0;32m'}
RED=${RED:-'\033[0;31m'}
YELLOW=${YELLOW:-'\033[0;33m'}
BLUE=${BLUE:-'\033[0;34m'}
NC=${NC:-'\033[0m'}

export HOST_UID="${HOST_UID:-$(id -u)}"
export HOST_GID="${HOST_GID:-$(id -g)}"

if [ -z "${E2E_PROJECT_NAME:-}" ]; then
    echo -e "${RED}Error: E2E_PROJECT_NAME must be defined before loading E2E helpers${NC}" >&2
    return 1 2>/dev/null || exit 1
fi

if [ -z "${E2E_DATA_DIR:-}" ]; then
    echo -e "${RED}Error: E2E_DATA_DIR must be defined before loading E2E helpers${NC}" >&2
    return 1 2>/dev/null || exit 1
fi

E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$E2E_ROOT/docker-compose.e2e.yml"

# Kept as a string because the existing cases expand it directly.
COMPOSE_CMD="docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE"

e2e_compose() {
    docker compose -p "$E2E_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

cleanup_e2e() {
    echo -e "${YELLOW}[$E2E_PROJECT_NAME] Cleaning up containers and directories...${NC}"
    e2e_compose down -v --remove-orphans >/dev/null 2>&1 || true
    docker network rm "${E2E_PROJECT_NAME}-net-b" >/dev/null 2>&1 || true

    if [ "${KEEP_E2E_DATA:-false}" != "true" ]; then
        rm -rf "$E2E_DATA_DIR" || true
    fi
}

exec_node() {
    local node_id=$1
    shift
    e2e_compose exec -T "$node_id" "$@"
}

run_node() {
    local node_id=$1
    shift
    e2e_compose run --rm -T "$node_id" "$@"
}

bootstrap_node() {
    local node_id=$1
    local port=$2

    mkdir -p "$E2E_DATA_DIR/$node_id/coverage"
    echo -e "🏗️ Initializing node '$node_id' on port $port..."
    run_node "$node_id" init --id "$node_id" --port "$port" --storage /app/data >/dev/null
}

node_port() {
    case "$1" in
        node-1) printf '%s\n' 8081 ;;
        node-2 | node-low-spec) printf '%s\n' 8082 ;;
        node-3) printf '%s\n' 8083 ;;
        *) printf '%s\n' 8080 ;;
    esac
}

start_node() {
    local node_id=$1
    local port=${2:-$(node_port "$node_id")}
    local timeout=${3:-${E2E_NODE_TIMEOUT:-60}}

    e2e_compose up -d "$node_id"
    wait_for_node "$timeout" "$node_id" "$port"
}

start_nodes() {
    local node_id

    e2e_compose up -d "$@"
    for node_id in "$@"; do
        wait_for_node "${E2E_NODE_TIMEOUT:-60}" "$node_id" "$(node_port "$node_id")"
    done
}

restart_node() {
    local node_id=$1
    local port=${2:-$(node_port "$node_id")}
    local timeout=${3:-${E2E_NODE_TIMEOUT:-60}}

    e2e_compose restart "$node_id"
    wait_for_node "$timeout" "$node_id" "$port"
}

join_cluster() {
    local node_id=$1
    local sponsor_id=$2
    local sponsor_port=$3
    local local_port=${4:-$(node_port "$node_id")}
    local invite_output token

    mkdir -p "$E2E_DATA_DIR/$node_id/coverage"
    wait_for_node "${E2E_NODE_TIMEOUT:-60}" "$sponsor_id" "$sponsor_port"

    echo -e "🎟️ [$sponsor_id]: Generating invitation token for $node_id..."
    invite_output=$(exec_node "$sponsor_id" ./proxyma cluster invite)
    token=$(printf '%s\n' "$invite_output" | tail -n 1 | tr -d '\r\n ')

    if [ -z "$token" ]; then
        echo -e "${RED}❌ Error generating invitation token on $sponsor_id${NC}" >&2
        return 1
    fi

    echo -e "🔗 [$node_id]: Joining the cluster..."
    run_node "$node_id" cluster join \
        --node_id "$node_id" \
        --token "$token" \
        --port "$local_port" >/dev/null
}

call_api() {
    local node_id=$1
    local method=$2
    local port=$3
    local path=${4#/}
    shift 4

    exec_node "$node_id" curl --fail --silent --show-error \
        --connect-timeout "${E2E_HTTP_CONNECT_TIMEOUT:-3}" \
        --max-time "${E2E_HTTP_TIMEOUT:-10}" \
        --cacert /app/data/certs/ca.crt \
        --cert "/app/data/certs/$node_id.crt" \
        --key "/app/data/certs/$node_id.key" \
        -X "$method" "$@" "https://localhost:$port/$path"
}

call_peer_api() {
    local client_id=$1
    local target_host=$2
    local method=$3
    local port=$4
    local path=${5#/}
    shift 5

    exec_node "$client_id" curl --fail --silent --show-error \
        --connect-timeout "${E2E_HTTP_CONNECT_TIMEOUT:-3}" \
        --max-time "${E2E_HTTP_TIMEOUT:-10}" \
        --cacert /app/data/certs/ca.crt \
        --cert "/app/data/certs/$client_id.crt" \
        --key "/app/data/certs/$client_id.key" \
        -X "$method" "$@" "https://$target_host:$port/$path"
}
