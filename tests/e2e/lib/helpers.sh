#!/bin/bash

# Colores estándar para E2E
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

# Autodetectar UID y GID para el mapeo de usuarios en Docker
export HOST_UID=${HOST_UID:-$(id -u)}
export HOST_GID=${HOST_GID:-$(id -g)}

# Verificar variables requeridas
if [ -z "$E2E_PROJECT_NAME" ]; then
    echo -e "${RED}Error: E2E_PROJECT_NAME debe estar definido antes de cargar helpers.sh${NC}"
    exit 1
fi

if [ -z "$E2E_DATA_DIR" ]; then
    echo -e "${RED}Error: E2E_DATA_DIR debe estar definido antes de cargar helpers.sh${NC}"
    exit 1
fi

# Ruta al compose E2E relativo a la ubicación del helper
COMPOSE_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docker-compose.e2e.yml"
COMPOSE_CMD="docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE"

cleanup_e2e() {
    echo -e "${YELLOW}[$E2E_PROJECT_NAME] Limpiando contenedores y directorios...${NC}"
    $COMPOSE_CMD down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$E2E_DATA_DIR" || true
    # Limpiar redes huérfanas creadas dinámicamente si las hay
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
    echo -e "🏗️ Inicializando nodo '$node_id' en puerto $port..."
    run_node "$node_id" init --id "$node_id" --port "$port" --storage "/app/data" >/dev/null
}

join_cluster() {
    local node_id=$1
    local sponsor_id=$2
    local sponsor_port=$3

    echo -e "🎟️ [$sponsor_id]: Generando token de invitación para $node_id..."
    local invite_output
    invite_output=$(exec_node "$sponsor_id" ./proxyma invite)
    local token
    token=$(echo "$invite_output" | grep -o "ey[a-zA-Z0-9._-]*")

    if [ -z "$token" ]; then
        echo -e "${RED}❌ Error al generar token de invitación en $sponsor_id${NC}"
        return 1
    fi

    echo -e "🔗 [$node_id]: Uniéndose al clúster..."
    run_node "$node_id" join --id "$node_id" --token "$token" >/dev/null
}

call_api() {
    local node_id=$1
    local method=$2
    local port=$3
    local path=$4
    shift 4 # Los argumentos adicionales se pasan directamente a curl

    exec_node "$node_id" curl -s \
        --cacert /app/data/certs/ca.crt \
        --cert "/app/data/certs/$node_id.crt" \
        --key "/app/data/certs/$node_id.key" \
        -X "$method" "$@" "https://localhost:$port/$path"
}
