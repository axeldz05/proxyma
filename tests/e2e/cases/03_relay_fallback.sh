#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_relay"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Iniciando caso de prueba: Relay Fallback bajo NAT virtual...${NC}"

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
mkdir -p "$E2E_DATA_DIR/node-2"
mkdir -p "$E2E_DATA_DIR/node-3"

# Inicializar y levantar clúster
bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

$COMPOSE_CMD up -d node-1
sleep 2

join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

$COMPOSE_CMD up -d node-2 node-3
sleep 2

# Registrar nombres de contenedores y redes
NODE1_CONTAINER=$($COMPOSE_CMD ps -q node-1)
NODE3_CONTAINER=$($COMPOSE_CMD ps -q node-3)
DEFAULT_NETWORK="${E2E_PROJECT_NAME}_proxyma-net"
NET_B_NAME="${E2E_PROJECT_NAME}-net-b"

echo "🌐 Creando red secundaria $NET_B_NAME..."
docker network create "$NET_B_NAME"

echo "🔗 Conectando node-3 y node-1 a $NET_B_NAME..."
docker network connect --alias node-3 "$NET_B_NAME" "$NODE3_CONTAINER"
docker network connect --alias node-1 "$NET_B_NAME" "$NODE1_CONTAINER"

echo "🚷 Desconectando node-3 de la red por defecto $DEFAULT_NETWORK..."
docker network disconnect "$DEFAULT_NETWORK" "$NODE3_CONTAINER"

# Esperar a que se asiente la topología de red y node-3 reestablezca el poll en la nueva red
sleep 20

# Escribir y subir un archivo a node-3
echo "Escribiendo archivo de prueba en node-3..."
echo "relay_fallback_works" > "$E2E_DATA_DIR/node-3/relay_test.txt"
call_api node-3 POST 8083 upload -F "file=@/app/data/relay_test.txt" > /dev/null

# Forzar sync en node-3 para que anuncie metadatos al sponsor (node-1)
exec_node node-3 ./proxyma sync > /dev/null

# Esperar a que los metadatos se propaguen a node-1
echo "🔍 Esperando propagación de metadatos de node-3 a node-1..."
if ! wait_for_condition 10 2 "relay_test.txt" call_api node-1 GET 8081 manifest; then
    echo -e "${RED}❌ Error: Los metadatos de node-3 no llegaron a node-1${NC}"
    exit 1
fi

# Suscribir node-2 a relay_test.txt
call_api node-2 POST 8082 "subscribe?name=relay_test.txt" > /dev/null

# Forzar sync en node-2
exec_node node-2 ./proxyma sync > /dev/null

# Obtener hash del archivo
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"relay_test.txt":{"name":"relay_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

if [ -z "$FILE_HASH" ]; then
    echo -e "${RED}❌ Error: No se encontró el hash de relay_test.txt en el manifest${NC}"
    exit 1
fi

# Esperar descarga en node-2 a través del relay de node-1
echo "📥 Descargando archivo en node-2 a través del Relay de node-1..."
if ! wait_for_condition 15 2 "relay_fallback_works" call_api node-2 GET 8082 "download/$FILE_HASH"; then
    echo -e "${RED}❌ Error: La descarga por Relay Fallback falló o no se completó.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Descarga por Relay Fallback exitosa. Contenido verificado.${NC}"
echo -e "${GREEN}🎉 Caso 3 (Relay Fallback) completado exitosamente!${NC}"
