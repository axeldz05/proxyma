#!/bin/bash

# Shared case teardown: capture sanitized diagnostics before deterministic cleanup.

e2e_finish_case() {
    local exit_code=$?

    trap - EXIT
    set +e
    if [ "$exit_code" -ne 0 ]; then
        dump_e2e_diagnostics "${E2E_CASE_FAILURE_REASON:-failure}"
    fi
    cleanup_e2e
    exit "$exit_code"
}

e2e_error_trap() {
    local exit_code=$?
    local line=$1
    local file=$2
    local cmd=$3
    echo -e "${RED:-}❌ [FAIL_LOC] $file:$line: command failed (exit $exit_code): $cmd${NC:-}" >&2
}

install_e2e_case_trap() {
    E2E_CASE_FAILURE_REASON=${1:-failure}
    trap 'e2e_error_trap "$LINENO" "${BASH_SOURCE[0]}" "$BASH_COMMAND"' ERR
    trap e2e_finish_case EXIT
}
