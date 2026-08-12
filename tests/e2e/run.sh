#!/bin/bash
set -uo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="$SCRIPTPATH/logs"
CASES_DIR="$SCRIPTPATH/cases"
PROFILES_DIR="$SCRIPTPATH/profiles"
COMPOSE_FILE="$SCRIPTPATH/docker-compose.e2e.yml"

# shellcheck source=lib/dump.sh
source "$SCRIPTPATH/lib/dump.sh"

mkdir -p "$LOG_DIR"
shopt -s nullglob

mapfile -t ALL_CASES < <(printf '%s\n' "$CASES_DIR"/*.sh | sort)
if [ ${#ALL_CASES[@]} -eq 0 ]; then
    echo -e "${RED}❌ No test cases found in $CASES_DIR${NC}" >&2
    exit 1
fi

trim() {
    local value=$1
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    printf '%s\n' "$value"
}

append_case_matches() {
    local selector=$1
    local matched=false test_case filename case_name

    for test_case in "${ALL_CASES[@]}"; do
        filename=$(basename "$test_case")
        case_name=${filename%.sh}
        if [[ "$case_name" == $selector || "$filename" == $selector ||
            "$case_name" == "$selector"_* ]]; then
            if [ -z "${SELECTED_SET[$test_case]:-}" ]; then
                TEST_CASES+=("$test_case")
                SELECTED_SET[$test_case]=1
            fi
            matched=true
        fi
    done

    if [ "$matched" != true ]; then
        echo -e "${RED}❌ No E2E case matches '$selector'${NC}" >&2
        return 1
    fi
}

declare -a TEST_CASES=()
declare -A SELECTED_SET=()

if [ -n "${E2E_CASE:-}" ]; then
    IFS=',' read -r -a case_selectors <<<"$E2E_CASE"
    for selector in "${case_selectors[@]}"; do
        selector=$(trim "$selector")
        [ -n "$selector" ] || continue
        append_case_matches "$selector" || exit 2
    done
else
    profile=${E2E_PROFILE:-all}
    if [ "$profile" = all ]; then
        TEST_CASES=("${ALL_CASES[@]}")
    else
        if ! [[ "$profile" =~ ^[A-Za-z0-9_-]+$ ]]; then
            echo -e "${RED}❌ Invalid E2E_PROFILE '$profile'${NC}" >&2
            exit 2
        fi
        profile_file="$PROFILES_DIR/$profile.txt"
        if [ ! -f "$profile_file" ]; then
            echo -e "${RED}❌ Unknown E2E profile '$profile'${NC}" >&2
            exit 2
        fi
        while IFS= read -r profile_line || [ -n "$profile_line" ]; do
            profile_line=${profile_line%%#*}
            profile_line=$(trim "$profile_line")
            [ -n "$profile_line" ] || continue
            append_case_matches "$profile_line" || exit 2
        done <"$profile_file"
    fi
fi

if [ ${#TEST_CASES[@]} -eq 0 ]; then
    echo -e "${RED}❌ E2E selection is empty${NC}" >&2
    exit 2
fi

parallel=${E2E_PARALLEL:-3}
if ! [[ "$parallel" =~ ^[1-9][0-9]*$ ]]; then
    echo -e "${RED}❌ E2E_PARALLEL must be a positive integer${NC}" >&2
    exit 2
fi

if [ "${E2E_LIST:-false}" = true ]; then
    for test_case in "${TEST_CASES[@]}"; do
        basename "$test_case" .sh
    done
    exit 0
fi

echo -e "${BLUE}======================================================${NC}"
echo -e "${BLUE}🧪  Proxyma E2E Test Runner                           ${NC}"
echo -e "${BLUE}======================================================${NC}"
echo "Selected ${#TEST_CASES[@]} case(s); parallel limit: $parallel"

export HOST_UID="${HOST_UID:-$(id -u)}"
export HOST_GID="${HOST_GID:-$(id -g)}"

if [ "${E2E_SKIP_BUILD:-false}" != true ]; then
    echo "🔨 Building the shared E2E image once..."
    E2E_PROJECT_NAME=e2e-image-build \
        E2E_DATA_DIR="${TMPDIR:-/tmp}/proxyma-e2e/image-build" \
        docker compose -p e2e-image-build -f "$COMPOSE_FILE" build node-1 || {
            echo -e "${RED}❌ Shared E2E image build failed${NC}" >&2
            exit 1
        }
fi

FAILED_COUNT=0
PASSED_COUNT=0
FAILED_NAMES=()
ARCHIVE_DIR="$LOG_DIR/failed/$(date +%Y%m%d-%H%M%S)"

declare -a ACTIVE_PIDS=()
declare -a ACTIVE_NAMES=()
declare -a ACTIVE_LOGS=()

reap_first() {
    local pid=${ACTIVE_PIDS[0]}
    local case_name=${ACTIVE_NAMES[0]}
    local log_file=${ACTIVE_LOGS[0]}
    local exit_code

    wait "$pid"
    exit_code=$?
    sanitize_file "$log_file" || true

    if [ "$exit_code" -eq 0 ]; then
        echo -e "🟢 [PASS] $case_name"
        PASSED_COUNT=$((PASSED_COUNT + 1))
    else
        echo -e "🔴 [FAIL] $case_name (exit $exit_code)"
        FAILED_COUNT=$((FAILED_COUNT + 1))
        FAILED_NAMES+=("$case_name")
        mkdir -p "$ARCHIVE_DIR"
        cp "$log_file" "$ARCHIVE_DIR/"
        echo -e "${YELLOW}--- Last 15 sanitized log lines for $case_name ---${NC}"
        tail -n 15 "$log_file"
        echo -e "${YELLOW}-------------------------------------------------${NC}"
    fi

    ACTIVE_PIDS=("${ACTIVE_PIDS[@]:1}")
    ACTIVE_NAMES=("${ACTIVE_NAMES[@]:1}")
    ACTIVE_LOGS=("${ACTIVE_LOGS[@]:1}")
}

stop_active() {
    local pid log_file
    trap - INT TERM
    for pid in "${ACTIVE_PIDS[@]}"; do
        kill "$pid" >/dev/null 2>&1 || true
    done
    for pid in "${ACTIVE_PIDS[@]}"; do
        wait "$pid" >/dev/null 2>&1 || true
    done
    for log_file in "${ACTIVE_LOGS[@]}"; do
        sanitize_file "$log_file" || true
    done
    exit 130
}
trap stop_active INT TERM

for test_case in "${TEST_CASES[@]}"; do
    case_name=$(basename "$test_case" .sh)
    log_file="$LOG_DIR/${case_name}.log"

    while [ ${#ACTIVE_PIDS[@]} -ge "$parallel" ]; do
        reap_first
    done

    echo "🛫 [START] $case_name (logs/${case_name}.log)"
    "$test_case" >"$log_file" 2>&1 &
    ACTIVE_PIDS+=("$!")
    ACTIVE_NAMES+=("$case_name")
    ACTIVE_LOGS+=("$log_file")
done

while [ ${#ACTIVE_PIDS[@]} -gt 0 ]; do
    reap_first
done

echo -e "${BLUE}======================================================${NC}"
echo "E2E results: $PASSED_COUNT passed, $FAILED_COUNT failed"
echo -e "${BLUE}======================================================${NC}"

if [ "$FAILED_COUNT" -ne 0 ]; then
    echo -e "Failed cases: ${RED}${FAILED_NAMES[*]}${NC}"
    echo -e "Sanitized logs: ${YELLOW}${ARCHIVE_DIR#"$SCRIPTPATH/"}${NC}"
    exit 1
fi

echo -e "${GREEN}🎉 All selected E2E cases passed.${NC}"
