#!/bin/bash
set -eo pipefail

# Configuración del proyecto E2E
export E2E_PROJECT_NAME="e2e_sync_ocr"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

# Cargar helpers
SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Iniciando caso de prueba: Sincronización y OCR básico...${NC}"

# Limpieza inicial
cleanup_e2e
trap cleanup_e2e EXIT

# Crear directorios
mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"
mkdir -p "$E2E_DATA_DIR/node-3"

# Generar PDF de prueba
echo "JVBERi0xLjQKMSAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMgMiAwIFIKPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9LaWRzIFszIDAgUl0KL0NvdW50IDEKPj4KZW5kb2JqCjMgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1BhcmVudCAyIDAgUgovTWVkaWFCb3ggWzAgMCA1OTUuMjggODQxLjg5XQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA0IDAgUgo+Pgo+PgovQ29udGVudHMgNSAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago1IDAgb2JqCjw8Ci9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCi9GMSAyNCBUZgoxMDAgNzAwIFRkCihIZWxsbyBQcm94eW1hIENsdXN0ZXIhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCnhyZWYKMCA2CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAwOSAwMDAwMCBuIAowMDAwMDAwMDU2IDAwMDAwIG4gCjAwMDAwMDAxMTEgMDAwMDAgbiAKMDAwMDAwMDIxMiAwMDAwMCBuIAowMDAwMDAwMjk5IDAwMDAwIG4gCnRyYWlsZXIKPDwKL1NpemUgNgovUm9vdCAxIDAgUgo+PgpzdGFydHhyZWYKMzkzCiUlRU9GCg==" | base64 -d > "$E2E_DATA_DIR/node-1/test_e2e.pdf"

# Generar definición de servicio local
cat << 'EOF' > "$E2E_DATA_DIR/node-2/services.json"
{
    "ocr": {
        "type": "script",
        "exec": "python3 /app/data/scripts/ocr_service.py",
        "schema": {
            "name": "ocr",
            "description": "OCR my PDF",
            "parameters": {
                "file_hash": {"type": "string", "description": "Hash of the PDF"}
            }
        }
    }
}
EOF

# Generar script de python para OCR
cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/ocr_service.py"
import sys, json, subprocess, os
try:
    payload = json.load(sys.stdin)
    file_hash = payload.get("file_hash")
    input_path = f"/app/data/{file_hash}"
    output_path = "/tmp/optimized.pdf"
    
    subprocess.run(["ocrmypdf", "--force-ocr", input_path, output_path], check=True, capture_output=True)
    
    upload_cmd = ["curl", "-s", "-X", "POST", "-F", f"file=@{output_path}", "https://localhost:8082/upload", "--cacert", "/app/data/certs/ca.crt", "--cert", "/app/data/certs/node-2.crt", "--key", "/app/data/certs/node-2.key"]
    res = subprocess.run(upload_cmd, capture_output=True, text=True, check=True)
    
    print(json.dumps({"status": "success", "upload_response": res.stdout}))
except Exception as e:
    print(json.dumps({"error": str(e)}))
EOF

# Inicializar nodos
bootstrap_node node-1 8081
bootstrap_node node-2 8082
bootstrap_node node-3 8083

# Levantar Nodo 1
$COMPOSE_CMD up -d node-1
sleep 2

# Emparejar e ingresar al clúster
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

# Levantar nodos secundarios
$COMPOSE_CMD up -d node-2 node-3
sleep 2

# Subir archivos a node-1
echo "Hello Proxyma Cluster!" > "$E2E_DATA_DIR/node-1/test_e2e.txt"
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.txt" > /dev/null
call_api node-1 POST 8081 upload -F "file=@/app/data/test_e2e.pdf" > /dev/null

# Disparar sincronización manual en node-3
exec_node node-3 ./proxyma sync > /dev/null

# Verificar sincronización de metadatos en node-3 VFS
echo "🔍 Verificando réplica de metadatos en node-3..."
MAX_RETRIES=10
FILE_FOUND=false
MANIFEST=""
for i in $(seq 1 $MAX_RETRIES); do
    MANIFEST=$(call_api node-3 GET 8083 manifest) || MANIFEST=""
    if echo "$MANIFEST" | grep -q "test_e2e.txt"; then
        FILE_FOUND=true
        break
    fi
    echo "   ... VFS no actualizado aún (reintentando $i/$MAX_RETRIES)..."
    sleep 2
done

if [ "$FILE_FOUND" != "true" ]; then
    echo -e "${RED}❌ Error: El archivo no llegó al VFS de node-3${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Metadatos sincronizados en VFS correctamente.${NC}"

# Obtener hash del archivo
FILE_HASH=$(echo "$MANIFEST" | grep -o '"test_e2e.txt":{"name":"test_e2e.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

# Verificar que no se descargó físicamente el blob sin estar suscrito
if [ -f "$E2E_DATA_DIR/node-3/$FILE_HASH" ]; then
    echo -e "${RED}❌ Error lógico: Nodo 3 descargó el blob sin suscripción.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Nodo 3 ignoró la descarga física (comportamiento esperado).${NC}"

# Suscribir node-3
SUB_RES=$(call_api node-3 POST 8083 "subscribe?name=test_e2e.txt")
if [[ -z "$SUB_RES" || "$SUB_RES" == *"error"* ]]; then
    echo -e "${RED}❌ Error al suscribirse en node-3${NC}"
    exit 1
fi

# Sincronizar de nuevo para forzar la descarga física
exec_node node-3 ./proxyma sync > /dev/null

# Verificar descarga y comprobar hash
call_api node-3 GET 8083 "download/$FILE_HASH" > "$E2E_DATA_DIR/node-3/downloaded_test.txt"
if ! diff "$E2E_DATA_DIR/node-1/test_e2e.txt" "$E2E_DATA_DIR/node-3/downloaded_test.txt" > /dev/null; then
    echo -e "${RED}❌ Error: Datos corruptos en la descarga de node-3.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Descarga e integridad criptográfica confirmadas.${NC}"

# Test OCR
echo "⚡ Ejecutando test OCR..."
MANIFEST_N1=$(call_api node-1 GET 8081 manifest)
PDF_HASH=$(echo "$MANIFEST_N1" | grep -o '"test_e2e.pdf":{"name":"test_e2e.pdf","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

call_api node-2 POST 8082 "subscribe?name=test_e2e.pdf" > /dev/null
exec_node node-2 ./proxyma sync > /dev/null

# Enviar tarea de OCR a node-2
call_api node-2 POST 8082 "services/submit" -d "{\"service\":\"ocr\", \"task_id\":\"ocr_job_1\", \"requester_node_id\":\"host-test\", \"payload\":{\"file_hash\":\"$PDF_HASH\"}}" > /dev/null

# Esperar a que el PDF procesado con OCR aparezca en el manifest de node-3
OCR_FOUND=false
for i in $(seq 1 30); do
    MANIFEST_N3=$(call_api node-3 GET 8083 manifest) || MANIFEST_N3=""
    if echo "$MANIFEST_N3" | grep -q "optimized.pdf"; then
        OCR_FOUND=true
        break
    fi
    echo "   ... OCR no subido aún (reintentando $i/30)..."
    sleep 2
done

if [ "$OCR_FOUND" != "true" ]; then
    echo -e "${RED}❌ Error: OCR falló o el archivo no se propagó.${NC}"
    exit 1
fi

echo -e "${GREEN}🎉 Caso 1 (Sync & OCR) completado exitosamente!${NC}"
