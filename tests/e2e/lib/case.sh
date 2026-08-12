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

install_e2e_case_trap() {
    E2E_CASE_FAILURE_REASON=${1:-failure}
    trap e2e_finish_case EXIT
}
