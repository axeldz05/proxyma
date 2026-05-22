#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_partition"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Iniciando caso de prueba: Partición de red...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

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

# Registrar nombres de contenedores y red
NODE3_CONTAINER=$(docker compose -p $E2E_PROJECT_NAME -f $COMPOSE_FILE ps -q node-3)
NETWORK_NAME="${E2E_PROJECT_NAME}_proxyma-net"

# 1. Verificar sincronización en red normal
echo "Escribiendo archivo inicial para verificar estado inicial..."
echo "base_state" > "$E2E_DATA_DIR/node-1/base.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/base.txt" > /dev/null

exec_node node-2 ./proxyma sync > /dev/null
exec_node node-3 ./proxyma sync > /dev/null

# Confirmar estado base
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if ! echo "$MANIFEST_N3" | grep -q "base.txt"; then
    echo -e "${RED}❌ Error: Sincronización base fallida${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Estado base verificado en todos los nodos.${NC}"

# 2. Provocar partición: Aislar nodo-3
echo "Disconnecting node-3 from network $NETWORK_NAME..."
docker network disconnect "$NETWORK_NAME" "$NODE3_CONTAINER"

# 3. Escrituras divergentes en la partición
echo "Escribiendo archivo A en la partición conectada (node-1)..."
echo "data_partition_a" > "$E2E_DATA_DIR/node-1/partition_a.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/partition_a.txt" > /dev/null

echo "Escribiendo archivo B en el nodo aislado (node-3)..."
echo "data_partition_b" > "$E2E_DATA_DIR/node-3/partition_b.txt"
# Nota: La llamada al api es local dentro del contenedor node-3, por lo que funciona aunque no tenga red externa
call_api node-3 POST 8083 upload -F "file=@/app/data/partition_b.txt" > /dev/null

# 4. Sincronizar y verificar que NO se propaga la información
echo "Tratando de sincronizar mientras existe la partición..."
exec_node node-2 ./proxyma sync > /dev/null || true
# Nota: Esto puede fallar o pasar sin error pero sin actualizar node-3, es correcto.

# Comprobar que node-1/2 no conocen partition_b.txt
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
if echo "$MANIFEST_N1" | grep -q "partition_b.txt"; then
    echo -e "${RED}❌ Error: Fuga de datos a través de la partición (node-1 conoce partition_b.txt)${NC}"
    exit 1
fi

# Comprobar que node-3 no conoce partition_a.txt
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if echo "$MANIFEST_N3" | grep -q "partition_a.txt"; then
    echo -e "${RED}❌ Error: Fuga de datos a través de la partición (node-3 conoce partition_a.txt)${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Nodos aislados correctamente. No hay transferencia de metadatos.${NC}"

# 5. Sanar partición: Conectar nodo-3
echo "Reconnecting node-3 to network $NETWORK_NAME..."
docker network connect "$NETWORK_NAME" "$NODE3_CONTAINER"
sleep 2

# 6. Sincronizar clúster sanado
echo "Disparando sincronización tras reconexión..."
exec_node node-3 ./proxyma sync > /dev/null
exec_node node-1 ./proxyma sync > /dev/null

# 7. Verificar convergencia
echo "🔍 Verificando convergencia de metadatos en node-1..."
MAX_RETRIES=10
CONVERGED=false
for i in $(seq 1 $MAX_RETRIES); do
    MANIFEST_N1=$(call_api node-1 GET 8081 manifest) || MANIFEST_N1=""
    if echo "$MANIFEST_N1" | grep -q "partition_a.txt" && echo "$MANIFEST_N1" | grep -q "partition_b.txt"; then
        CONVERGED=true
        break
    fi
    echo "   ... Esperando convergencia ($i/$MAX_RETRIES)..."
    sleep 2
done

if [ "$CONVERGED" != "true" ]; then
    echo -e "${RED}❌ Error: El clúster no recuperó consistencia tras sanar la partición.${NC}"
    exit 1
fi

# Verificar en node-3
MANIFEST_N3=$(call_api node-3 GET 8083 manifest)
if ! echo "$MANIFEST_N3" | grep -q "partition_a.txt" || ! echo "$MANIFEST_N3" | grep -q "partition_b.txt"; then
    echo -e "${RED}❌ Error: El nodo 3 reconectado no recibió los metadatos globales.${NC}"
    exit 1
fi

echo -e "${GREEN}🎉 Caso 2 (Partición de red) completado exitosamente!${NC}"
