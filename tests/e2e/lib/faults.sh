#!/bin/bash

# Docker fault injection primitives. Functional assertions must still use the
# public Proxyma CLI/API helpers from cluster.sh.

e2e_container_id() {
    e2e_compose ps -q "$1"
}

e2e_default_network() {
    printf '%s_proxyma-net\n' "$E2E_PROJECT_NAME"
}

disconnect_node() {
    local node_id=$1
    local network=${2:-$(e2e_default_network)}
    local container

    container=$(e2e_container_id "$node_id")
    if [ -z "$container" ]; then
        echo "Cannot disconnect $node_id: container not found" >&2
        return 1
    fi
    docker network disconnect "$network" "$container"
}

reconnect_node() {
    local node_id=$1
    local network=${2:-$(e2e_default_network)}
    local alias=${3:-$node_id}
    local container

    container=$(e2e_container_id "$node_id")
    if [ -z "$container" ]; then
        echo "Cannot reconnect $node_id: container not found" >&2
        return 1
    fi
    docker network connect --alias "$alias" "$network" "$container"
}

kill_node() {
    e2e_compose kill "$1"
}

pause_node() {
    e2e_compose pause "$1"
}

unpause_node() {
    e2e_compose unpause "$1"
}

create_fault_network() {
    local network=${1:-"${E2E_PROJECT_NAME}-net-b"}
    docker network create "$network"
}

remove_fault_network() {
    local network=${1:-"${E2E_PROJECT_NAME}-net-b"}
    docker network rm "$network"
}
