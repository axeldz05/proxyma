#!/bin/bash

# Standard console colors
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

# Find all test scripts in cases/
TEST_CASES=($(find "$CASES_DIR" -name "*.sh" | sort))

if [ ${#TEST_CASES[@]} -eq 0 ]; then
    echo -e "${RED}❌ No test cases found in $CASES_DIR${NC}"
    exit 1
fi

declare -A PIDS
declare -A LOG_FILES
declare -A CASE_NAMES

echo -e "Found ${#TEST_CASES[@]} test cases. Starting in parallel...\n"

for test_case in "${TEST_CASES[@]}"; do
    case_name=$(basename "$test_case" .sh)
    log_file="$LOG_DIR/${case_name}.log"
    
    echo -e "🛫 [Starting] $case_name (logging to logs/${case_name}.log)"
    
    # Execute in background redirecting stdout and stderr to the log file
    "$test_case" > "$log_file" 2>&1 &
    pid=$!
    
    PIDS[$pid]=$case_name
    LOG_FILES[$case_name]=$log_file
    CASE_NAMES[$pid]=$case_name
done

echo -e "\n⏳ Waiting for all tests to finish...\n"

FAILED_COUNT=0
PASSED_COUNT=0
FAILED_NAMES=()

# The next run overwrites logs/, so keep a copy of anything that failed.
ARCHIVE_DIR="$LOG_DIR/failed/$(date +%Y%m%d-%H%M%S)"

for pid in "${!CASE_NAMES[@]}"; do
    case_name=${CASE_NAMES[$pid]}
    log_file=${LOG_FILES[$case_name]}
    
    # Wait for specific PID and capture exit code
    wait $pid
    exit_code=$?
    
    if [ $exit_code -eq 0 ]; then
        echo -e "🟢 [PASS] ${case_name}"
        PASSED_COUNT=$((PASSED_COUNT + 1))
    else
        echo -e "🔴 [FAIL] ${case_name} (Exit code: $exit_code)"
        FAILED_COUNT=$((FAILED_COUNT + 1))
        FAILED_NAMES+=("$case_name")
        mkdir -p "$ARCHIVE_DIR"
        cp "$log_file" "$ARCHIVE_DIR/"
        echo -e "${YELLOW}--- Last 15 lines of log for $case_name: ---${NC}"
        tail -n 15 "$log_file"
        echo -e "${YELLOW}----------------------------------------------${NC}\n"
    fi
done

echo -e "\n${BLUE}======================================================${NC}"
echo -e "📊  E2E Suite Results:"
echo -e "    Passed:   ${GREEN}$PASSED_COUNT${NC}"
echo -e "    Failed:  ${RED}$FAILED_COUNT${NC}"
echo -e "${BLUE}======================================================${NC}"

if [ $FAILED_COUNT -ne 0 ]; then
    # Repeat the names here: the per-case output above scrolls away, and truncating
    # the tail of this script's output used to lose which case actually failed.
    echo -e "    Failed cases: ${RED}${FAILED_NAMES[*]}${NC}"
    echo -e "    Logs kept in: ${YELLOW}${ARCHIVE_DIR#"$SCRIPTPATH/"}${NC}"
    echo -e "${RED}❌ E2E test suite failed.${NC}"
    exit 1
else
    echo -e "${GREEN}🎉 All test cases passed successfully!${NC}"
    exit 0
fi
