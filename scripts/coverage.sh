#!/bin/bash
set -e

# Colores para la terminal
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
NC='\033[0m'

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPTPATH/.."

cd "$PROJECT_ROOT"

# Clean up any existing coverage files
rm -f coverage-unit.out coverage-e2e.out coverage.out
rm -rf /tmp/proxyma-merged-covdata /tmp/proxyma-e2e

echo -e "${BLUE}======================================================${NC}"
echo -e "${BLUE}📊 Unified Test Coverage Generator                     ${NC}"
echo -e "${BLUE}======================================================${NC}"

# 1. Build E2E images with coverage instrumentation
echo -e "\n${BLUE}🐳 [1/5] Building E2E containers with coverage enabled...${NC}"
COVER=true docker compose -f tests/e2e/docker-compose.e2e.yml build

# 2. Run E2E tests and preserve coverage outputs
echo -e "\n${BLUE}🛫 [2/5] Running E2E integration tests...${NC}"
export KEEP_E2E_DATA=true
if ! ./tests/e2e/run.sh; then
    echo -e "${RED}❌ E2E tests failed. Unified coverage canceled.${NC}"
    exit 1
fi

# 3. Locate and merge E2E coverage directories
echo -e "\n${BLUE}🧩 [3/5] Merging E2E coverage data...${NC}"
COV_DIRS=$(find /tmp/proxyma-e2e -type d -name "coverage" 2>/dev/null | paste -sd "," - || true)

if [ -n "$COV_DIRS" ]; then
    mkdir -p /tmp/proxyma-merged-covdata
    go tool covdata merge -i="$COV_DIRS" -o=/tmp/proxyma-merged-covdata
    go tool covdata textfmt -i=/tmp/proxyma-merged-covdata -o=coverage-e2e.out
    echo -e "${GREEN}✅ E2E coverage profile generated (coverage-e2e.out).${NC}"
else
    echo -e "${YELLOW}⚠️ No E2E coverage data directories found.${NC}"
    touch coverage-e2e.out
fi

# 4. Run Go unit tests and generate unit coverage profile
echo -e "\n${BLUE}🧪 [4/5] Running Go unit tests...${NC}"
if ! go test -coverprofile=coverage-unit.out ./...; then
    echo -e "${RED}❌ Unit tests failed. Unified coverage canceled.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Unit coverage profile generated (coverage-unit.out).${NC}"

# 5. Merge unit and E2E profiles using merge_profiles.go
echo -e "\n${BLUE}🔗 [5/5] Merging unit and E2E coverage profiles...${NC}"
go run scripts/merge_profiles.go coverage.out coverage-unit.out coverage-e2e.out

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}📊 Combined Overall Coverage Report:${NC}"
echo -e "${GREEN}======================================================${NC}"
go tool cover -func=coverage.out

# Clean up temporary E2E directories
echo -e "\n${YELLOW}🧹 Cleaning up temporary coverage and E2E data...${NC}"
rm -rf /tmp/proxyma-merged-covdata /tmp/proxyma-e2e
echo -e "${GREEN}✨ Clean complete. Combined profile saved to coverage.out${NC}"
