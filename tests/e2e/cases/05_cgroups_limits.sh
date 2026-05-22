#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_cgroups"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Iniciando caso de prueba: Límites de Recursos cgroups...${NC}"

cleanup_on_exit() {
    local exit_code=$?
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}❌ Test failed with exit code $exit_code. Keeping containers for inspection.${NC}"
    else
        cleanup_e2e
    fi
}
trap cleanup_on_exit EXIT

# Limpieza inicial
cleanup_e2e

# Crear directorios
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2" # node-low-spec uses node-2 data volume mapping

# Inicializar y levantar Sponsor
bootstrap_node node-1 8081
$COMPOSE_CMD up -d node-1
sleep 2

# Inicializar y unir node-low-spec
bootstrap_node node-low-spec 8082
join_cluster node-low-spec node-1 8081

# Levantar node-low-spec
$COMPOSE_CMD up -d node-low-spec
sleep 2

# Consultar endpoint de telemetría en node-low-spec
echo "🔍 Consultando endpoint de telemetría..."
TELEMETRY=$(call_api node-low-spec GET 8082 telemetry)
echo "Telemetría recibida: $TELEMETRY"

# Verificar que reporta límites de cgroups reales
# Esperamos cpu_limit = 0.5 y memory_limit = 536870912 (512MB)
CPU_LIMIT=$(echo "$TELEMETRY" | grep -o '"cpu_limit":[^,}]*' | cut -d':' -f2)
MEM_LIMIT=$(echo "$TELEMETRY" | grep -o '"memory_limit":[^,}]*' | cut -d':' -f2)

echo "CPU Limit obtenido: $CPU_LIMIT"
echo "Memory Limit obtenido: $MEM_LIMIT"

# Verificar CPU limit (debe ser 0.5)
if [ "$CPU_LIMIT" != "0.5" ] && [ "$CPU_LIMIT" != "0.50" ]; then
    echo -e "${RED}❌ Error: Límite de CPU incorrecto. Esperado 0.5, obtenido $CPU_LIMIT${NC}"
    exit 1
fi

# Verificar Memory limit (debe ser 536870912)
if [ "$MEM_LIMIT" != "536870912" ]; then
    echo -e "${RED}❌ Error: Límite de Memoria incorrecto. Esperado 536870912, obtenido $MEM_LIMIT${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Límites de cgroups reportados correctamente por telemetría (CPU: 0.5, RAM: 512MB).${NC}"
echo -e "${GREEN}🎉 Caso 5 (cgroups limits) completado exitosamente!${NC}"
