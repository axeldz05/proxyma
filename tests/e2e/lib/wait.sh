#!/bin/bash

# Deadline-bounded polling. New helpers use exponential backoff; the legacy
# wait_for_condition contract remains available for unmigrated cases.

E2E_LAST_OUTPUT=${E2E_LAST_OUTPUT:-}

_e2e_millis_sleep() {
    local millis=$1
    printf -v _e2e_sleep_seconds '%d.%03d' "$((millis / 1000))" "$((millis % 1000))"
    sleep "$_e2e_sleep_seconds"
}

_e2e_next_delay() {
    local delay=$1
    local maximum=$2
    local next=$((delay * 2))

    if ((next > maximum)); then
        next=$maximum
    fi
    printf '%s\n' "$next"
}

_e2e_timeout_message() {
    local description=$1
    local timeout=$2

    echo "Timed out after ${timeout}s waiting for ${description}." >&2
    _e2e_last_output_message
}

_e2e_last_output_message() {
    if [ -n "${E2E_LAST_OUTPUT:-}" ]; then
        echo "Last public response:" >&2
        if declare -F e2e_sanitize >/dev/null; then
            printf '%s\n' "$E2E_LAST_OUTPUT" | e2e_sanitize >&2
        else
            printf '%s\n' "$E2E_LAST_OUTPUT" >&2
        fi
    fi
}

wait_until() {
    local timeout=$1
    local description=$2
    shift 2

    if ! [[ "$timeout" =~ ^[1-9][0-9]*$ ]]; then
        echo "wait_until timeout must be a positive integer: $timeout" >&2
        return 2
    fi

    local deadline=$((SECONDS + timeout))
    local delay=${E2E_POLL_INITIAL_MS:-250}
    local max_delay=${E2E_POLL_MAX_MS:-2000}
    local output

    E2E_LAST_OUTPUT=
    while :; do
        if output=$("$@" 2>&1); then
            E2E_LAST_OUTPUT=$output
            printf '%s\n' "$output"
            return 0
        else
            local probe_status=$?
        fi
        E2E_LAST_OUTPUT=$output

        if [ "$probe_status" -eq 125 ]; then
            echo "Terminal failure while waiting for ${description}." >&2
            _e2e_last_output_message
            return 1
        fi

        if ((SECONDS >= deadline)); then
            _e2e_timeout_message "$description" "$timeout"
            return 1
        fi

        local remaining_ms=$(((deadline - SECONDS) * 1000))
        local sleep_ms=$delay
        if ((sleep_ms > remaining_ms)); then
            sleep_ms=$remaining_ms
        fi
        _e2e_millis_sleep "$sleep_ms"
        delay=$(_e2e_next_delay "$delay" "$max_delay")
    done
}

_e2e_output_contains() {
    local expected=$1
    shift
    local output

    output=$("$@" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [[ "$output" == *"$expected"* ]]
}

wait_for_output() {
    local timeout=$1
    local expected=$2
    shift 2

    wait_until "$timeout" "output containing '$expected'" \
        _e2e_output_contains "$expected" "$@"
}

_e2e_node_ready() {
    local node_id=$1
    local port=$2
    local output

    output=$(call_api "$node_id" GET "$port" telemetry 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [[ "$output" == *'"node_id"'* && "$output" == *"$node_id"* ]]
}

wait_for_node() {
    local timeout=$1
    local node_id=$2
    local port=$3

    wait_until "$timeout" "public mTLS health of $node_id" \
        _e2e_node_ready "$node_id" "$port" >/dev/null
}

_e2e_peer_visible() {
    local observer_id=$1
    local expected_peer_id=$2
    local storage=$3
    local output

    output=$(exec_node "$observer_id" ./proxyma peers list --storage "$storage" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    [[ "$output" == *"$expected_peer_id"* ]]
}

wait_for_peer() {
    local timeout=$1
    local observer_id=$2
    local expected_peer_id=$3
    local storage=${4:-/app/data}

    wait_until "$timeout" "$observer_id to list peer $expected_peer_id" \
        _e2e_peer_visible "$observer_id" "$expected_peer_id" "$storage" >/dev/null
}

_e2e_task_status() {
    local node_id=$1
    local storage=$2
    local output

    output=$(exec_node "$node_id" ./proxyma service status --storage "$storage" 2>&1) || {
        printf '%s\n' "$output"
        return 1
    }
    printf '%s\n' "$output"
    if [[ "$output" == *failed* ]]; then
        return 125
    fi
    [[ "$output" == *completed* ]]
}

wait_for_task_completed() {
    local timeout=$1
    local node_id=$2
    local storage=${3:-/app/data}
    local output

    if ! output=$(wait_until "$timeout" "a completed task on $node_id" \
        _e2e_task_status "$node_id" "$storage"); then
        return 1
    fi

    printf '%s\n' "$output"
}

wait_for_condition() {
    local max_retries=$1
    local delay=$2
    local expected=$3
    shift 3

    if ! [[ "$max_retries" =~ ^[1-9][0-9]*$ ]]; then
        echo "wait_for_condition retries must be a positive integer: $max_retries" >&2
        return 2
    fi

    local attempt output
    E2E_LAST_OUTPUT=
    for ((attempt = 1; attempt <= max_retries; attempt++)); do
        if output=$("$@") && printf '%s\n' "$output" | grep -q "$expected"; then
            E2E_LAST_OUTPUT=$output
            return 0
        fi
        E2E_LAST_OUTPUT=$output
        if ((attempt < max_retries)); then
            sleep "$delay"
        fi
    done
    return 1
}
