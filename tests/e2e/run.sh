#!/bin/bash

# Colores estándar para consola
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="$SCRIPTPATH/logs"
CASES_DIR="$SCRIPTPATH/cases"

mkdir -p "$LOG_DIR"

echo -e "${BLUE}======================================================${NC}"
echo -e "${BLUE}🧪  Proxyma E2E Parallel Test Runner                  ${NC}"
echo -e "${BLUE}======================================================${NC}"

# Buscar todos los scripts de prueba en cases/
TEST_CASES=($(find "$CASES_DIR" -name "*.sh" | sort))

if [ ${#TEST_CASES[@]} -eq 0 ]; then
    echo -e "${RED}❌ No se encontraron casos de prueba en $CASES_DIR${NC}"
    exit 1
fi

declare -A PIDS
declare -A LOG_FILES
declare -A CASE_NAMES

echo -e "Encontrados ${#TEST_CASES[@]} casos de prueba. Iniciando en paralelo...\n"

for test_case in "${TEST_CASES[@]}"; do
    case_name=$(basename "$test_case" .sh)
    log_file="$LOG_DIR/${case_name}.log"
    
    echo -e "🛫 [Iniciando] $case_name (registrando en logs/${case_name}.log)"
    
    # Ejecutar en segundo plano redireccionando stdout y stderr al archivo de logs
    "$test_case" > "$log_file" 2>&1 &
    pid=$!
    
    PIDS[$pid]=$case_name
    LOG_FILES[$case_name]=$log_file
    CASE_NAMES[$pid]=$case_name
done

echo -e "\n⏳ Esperando a que finalicen todas las pruebas...\n"

FAILED_COUNT=0
PASSED_COUNT=0

for pid in "${!CASE_NAMES[@]}"; do
    case_name=${CASE_NAMES[$pid]}
    log_file=${LOG_FILES[$case_name]}
    
    # Esperar al PID específico y capturar código de retorno
    wait $pid
    exit_code=$?
    
    if [ $exit_code -eq 0 ]; then
        echo -e "🟢 [PASS] ${case_name}"
        PASSED_COUNT=$((PASSED_COUNT + 1))
    else
        echo -e "🔴 [FAIL] ${case_name} (Código de salida: $exit_code)"
        FAILED_COUNT=$((FAILED_COUNT + 1))
        echo -e "${YELLOW}--- Últimas 15 líneas del log de $case_name: ---${NC}"
        tail -n 15 "$log_file"
        echo -e "${YELLOW}----------------------------------------------${NC}\n"
    fi
done

echo -e "\n${BLUE}======================================================${NC}"
echo -e "📊  Resultados de la Suite E2E:"
echo -e "    Pasados:   ${GREEN}$PASSED_COUNT${NC}"
echo -e "    Fallados:  ${RED}$FAILED_COUNT${NC}"
echo -e "${BLUE}======================================================${NC}"

if [ $FAILED_COUNT -ne 0 ]; then
    echo -e "${RED}❌ La suite de pruebas E2E falló.${NC}"
    exit 1
else
    echo -e "${GREEN}🎉 ¡Todos los casos de prueba pasaron exitosamente!${NC}"
    exit 0
fi
