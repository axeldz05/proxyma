#!/bin/bash

# Assertions operate on output returned by Proxyma's public CLI or HTTP API.

fail_assertion() {
    local message=$1
    local actual=${2:-}

    echo -e "${RED:-}❌ $message${NC:-}" >&2
    if [ -n "$actual" ]; then
        echo "Observed public output:" >&2
        if declare -F e2e_sanitize >/dev/null; then
            printf '%s\n' "$actual" | e2e_sanitize >&2
        else
            printf '%s\n' "$actual" >&2
        fi
    fi
    return 1
}

assert_contains() {
    local actual=$1
    local expected=$2
    local message=${3:-"Expected public output to contain '$expected'"}

    if [[ "$actual" != *"$expected"* ]]; then
        fail_assertion "$message" "$actual"
    fi
}

assert_not_contains() {
    local actual=$1
    local unexpected=$2
    local message=${3:-"Expected public output not to contain '$unexpected'"}

    if [[ "$actual" == *"$unexpected"* ]]; then
        fail_assertion "$message" "$actual"
    fi
}

assert_equals() {
    local actual=$1
    local expected=$2
    local message=${3:-"Expected '$expected', got '$actual'"}

    if [ "$actual" != "$expected" ]; then
        fail_assertion "$message" "$actual"
    fi
}

assert_not_empty() {
    local actual=$1
    local message=${2:-"Expected non-empty public output"}

    if [ -z "$actual" ]; then
        fail_assertion "$message"
    fi
}
