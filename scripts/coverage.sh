#!/bin/bash
set -euo pipefail

# Colores para la terminal
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
NC='\033[0m'

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPTPATH/.."

cd "$PROJECT_ROOT"

cleanup_coverage_data() {
    rm -rf /tmp/proxyma-merged-covdata /tmp/proxyma-e2e
}

trap cleanup_coverage_data EXIT

# Clean up any existing coverage files
rm -f coverage-unit.out coverage-e2e.out coverage.out
cleanup_coverage_data

echo -e "${BLUE}======================================================${NC}"
echo -e "${BLUE}📊 Unified Test Coverage Generator                     ${NC}"
echo -e "${BLUE}======================================================${NC}"

# 1. Build E2E images with coverage instrumentation
if [ "${E2E_SKIP_BUILD:-false}" = true ]; then
    echo -e "\n${BLUE}🐳 [1/5] Using the prebuilt coverage-enabled E2E image...${NC}"
else
    echo -e "\n${BLUE}🐳 [1/5] Building the shared E2E image with coverage enabled...${NC}"
    COVER=true docker compose -f tests/e2e/docker-compose.e2e.yml build node-1
fi

# 2. Run E2E tests and preserve coverage outputs
echo -e "\n${BLUE}🛫 [2/5] Running E2E integration tests...${NC}"
export E2E_PROFILE="${E2E_PROFILE:-full}"
if ! COVER=true KEEP_E2E_DATA=true E2E_SKIP_BUILD=true ./tests/e2e/run.sh; then
    echo -e "${RED}❌ E2E tests failed. Unified coverage canceled.${NC}"
    exit 1
fi

# 3. Locate and merge E2E coverage directories
echo -e "\n${BLUE}🧩 [3/5] Merging E2E coverage data...${NC}"
mapfile -t COV_DIR_CANDIDATES < <(find /tmp/proxyma-e2e -type d -name "coverage" 2>/dev/null | sort || true)
COV_DIR_ARRAY=()
for cov_dir in "${COV_DIR_CANDIDATES[@]}"; do
    if compgen -G "$cov_dir/covmeta.*" >/dev/null &&
        compgen -G "$cov_dir/covcounters.*" >/dev/null; then
        COV_DIR_ARRAY+=("$cov_dir")
    fi
done

HAS_E2E_COVERAGE=false
if [ ${#COV_DIR_ARRAY[@]} -gt 0 ]; then
    COV_DIRS=$(IFS=,; echo "${COV_DIR_ARRAY[*]}")
    mkdir -p /tmp/proxyma-merged-covdata
    go tool covdata merge -i="$COV_DIRS" -o=/tmp/proxyma-merged-covdata
    go tool covdata textfmt -i=/tmp/proxyma-merged-covdata -o=coverage-e2e.out
    HAS_E2E_COVERAGE=true
    echo -e "${GREEN}✅ E2E coverage profile generated (coverage-e2e.out).${NC}"
elif [ "${COVERAGE_ALLOW_MISSING_E2E:-false}" = true ]; then
    printf 'mode: set\n' > coverage-e2e.out
    echo -e "${YELLOW}⚠️ No E2E covdata found; continuing because COVERAGE_ALLOW_MISSING_E2E=true.${NC}"
else
    echo -e "${RED}❌ No E2E covdata found. Coverage generation requires covmeta and covcounters files.${NC}" >&2
    echo -e "${YELLOW}   For local unit-only runs, set COVERAGE_ALLOW_MISSING_E2E=true.${NC}" >&2
    exit 1
fi

# 4. Run Go unit tests and generate unit coverage profile
echo -e "\n${BLUE}🧪 [4/5] Running Go unit tests...${NC}"
if ! go test -count=1 -coverprofile=coverage-unit.out ./...; then
    echo -e "${RED}❌ Unit tests failed. Unified coverage canceled.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Unit coverage profile generated (coverage-unit.out).${NC}"

# 5. Merge unit and E2E profiles using merge_profiles.go
echo -e "\n${BLUE}🔗 [5/5] Merging unit and E2E coverage profiles...${NC}"
go run scripts/merge_profiles.go --strict coverage.out coverage-unit.out coverage-e2e.out

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}📊 Unit Coverage Report:${NC}"
echo -e "${GREEN}======================================================${NC}"
go tool cover -func=coverage-unit.out

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}📊 E2E Coverage Report:${NC}"
echo -e "${GREEN}======================================================${NC}"
if [ "$HAS_E2E_COVERAGE" != true ]; then
    echo -e "${YELLOW}No E2E covdata was generated; reporting the explicit empty profile.${NC}"
fi
go tool cover -func=coverage-e2e.out

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}📊 Union Coverage Report:${NC}"
echo -e "${GREEN}======================================================${NC}"
go tool cover -func=coverage.out

# Clean up temporary E2E directories
echo -e "\n${YELLOW}🧹 Cleaning up temporary coverage and E2E data...${NC}"
cleanup_coverage_data
echo -e "${GREEN}✨ Clean complete. Combined profile saved to coverage.out${NC}"
