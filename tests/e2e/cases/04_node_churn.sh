#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_churn"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Iniciando caso de prueba: Caída abrupta de nodos y Churn...${NC}"

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

# Suscribir node-1 (el Sponsor) a churn_test.txt para que mantenga una copia física
echo "📥 Suscribiendo node-1 (Sponsor) a churn_test.txt..."
call_api node-1 POST 8081 "subscribe?name=churn_test.txt" > /dev/null

# Crear un archivo de prueba grande (15 MB) en node-2
echo "📦 Generando archivo grande (15MB) en node-2..."
dd if=/dev/urandom of="$E2E_DATA_DIR/node-2/churn_test.txt" bs=1M count=15 2>/dev/null

# Subir el archivo grande a node-2
echo "📤 Subiendo archivo a node-2..."
call_api node-2 POST 8082 upload -F "file=@/app/data/churn_test.txt" > /dev/null

# Forzar sync en node-2 para anunciar el archivo al Sponsor
exec_node node-2 ./proxyma sync > /dev/null

# Esperar a que el archivo llegue al VFS de node-1 y se descargue
echo "🔍 Esperando que node-1 detecte y descargue el archivo..."
if ! wait_for_condition 15 2 "churn_test.txt" call_api node-1 GET 8081 manifest; then
    echo -e "${RED}❌ Error: El Sponsor no recibió los metadatos del archivo.${NC}"
    exit 1
fi

# Forzar sync en node-1 para descargar físicamente el archivo
exec_node node-1 ./proxyma sync > /dev/null

# Obtener hash del archivo
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
FILE_HASH=$(echo "$MANIFEST_N1" | grep -o '"churn_test.txt":{"name":"churn_test.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

if [ -z "$FILE_HASH" ]; then
    echo -e "${RED}❌ Error: No se pudo obtener el hash del archivo${NC}"
    exit 1
fi

# Verificar descarga física en node-1
echo "🔍 Verificando copia física en node-1..."
if ! wait_for_condition 10 1 "$FILE_HASH" exec_node node-1 ls "/app/data/$FILE_HASH"; then
    echo -e "${RED}❌ Error: El Sponsor no descargó la copia física del archivo.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ El Sponsor (node-1) tiene la copia física completa del archivo.${NC}"

# Suscribir node-3 (el cliente) al archivo
echo "📥 Suscribiendo node-3 al archivo..."
call_api node-3 POST 8083 "subscribe?name=churn_test.txt" > /dev/null

# Iniciar la sincronización en node-3 en segundo plano (para interrumpirla mid-download)
echo "⚡ Iniciando sincronización en node-3 en segundo plano..."
exec_node node-3 ./proxyma sync > /dev/null &
SYNC_PID=$!

# Esperar un momento breve para que comience la descarga desde node-2
sleep 1.5

# Matar node-2 abruptamente
echo "💥 Matando node-2 abruptamente (simulando corte de energía)..."
$COMPOSE_CMD kill node-2 >/dev/null

# Esperar a que el comando de fondo termine o falle
wait $SYNC_PID || true

# Como node-2 se murió a la mitad, la sincronización en node-3 pudo haber fallado
# o quedado incompleta. Vamos a forzar otra sincronización en node-3.
# Al no estar node-2, node-3 sólo debería poder bajar el resto/todo desde node-1 (el Sponsor).
echo "🔄 Forzando sincronización de recuperación en node-3..."
exec_node node-3 ./proxyma sync > /dev/null || true

# Esperar a que node-3 termine la descarga y verifique la integridad física
echo "🔍 Esperando a que node-3 complete la descarga desde node-1..."
if ! wait_for_condition 15 2 "$FILE_HASH" exec_node node-3 ls "/app/data/$FILE_HASH"; then
    echo -e "${RED}❌ Error: node-3 no pudo recuperar el archivo tras la caída de node-2.${NC}"
    exit 1
fi

# Descargar y verificar integridad binaria
call_api node-3 GET 8083 "download/$FILE_HASH" > "$E2E_DATA_DIR/node-3/downloaded_churn.txt"
if ! diff "$E2E_DATA_DIR/node-2/churn_test.txt" "$E2E_DATA_DIR/node-3/downloaded_churn.txt" > /dev/null; then
    echo -e "${RED}❌ Error: El archivo recuperado en node-3 está corrupto.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Recuperación por Node Churn exitosa. Archivo íntegro.${NC}"
echo -e "${GREEN}🎉 Caso 4 (Node Churn) completado exitosamente!${NC}"
