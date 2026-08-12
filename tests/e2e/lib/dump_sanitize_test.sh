#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/e2e/lib/dump.sh
source "$SCRIPT_DIR/dump.sh"

readonly V1_INVITE='eyJhZGRyZXNzIjoiaHR0cHM6Ly8xOTIuMC4yLjE6ODA4MCIsImNhX2hhc2giOiJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiIn0.cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd'
readonly V2_INVITE='Aqurq6urq6urq6urq6urq6urq6urq6urq6urq6urq6urzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc0fkAEBwAACAQ'
readonly SHA256='abababababababababababababababababababababababababababababababab'
readonly GIT_SHA='0123456789abcdef0123456789abcdef01234567'
readonly GENERIC_BASE64='AgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'

assert_sanitized() {
    local input=$1
    local expected=$2
    local actual

    actual=$(printf '%s\n' "$input" | e2e_sanitize)
    if [ "$actual" != "$expected" ]; then
        printf 'sanitize mismatch\ninput:    %s\nexpected: %s\nactual:   %s\n' \
            "$input" "$expected" "$actual" >&2
        return 1
    fi
}

assert_sanitized "$V1_INVITE" '[REDACTED INVITE TOKEN]'
assert_sanitized "  $V2_INVITE  " '[REDACTED INVITE TOKEN]'
assert_sanitized \
    '{"token":"invite-token","secret":"cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"}' \
    '{"token":"[REDACTED]","secret":"[REDACTED]"}'

assert_sanitized "$SHA256" "$SHA256"
assert_sanitized "ca_hash=$SHA256" "ca_hash=$SHA256"
assert_sanitized "{\"ca_hash\":\"$SHA256\"}" "{\"ca_hash\":\"$SHA256\"}"
assert_sanitized "GET /download/$SHA256 HTTP/1.1" "GET /download/$SHA256 HTTP/1.1"
assert_sanitized "$GIT_SHA refs/heads/main" "$GIT_SHA refs/heads/main"
assert_sanitized "$GENERIC_BASE64" "$GENERIC_BASE64"
assert_sanitized "prefix $V2_INVITE suffix" "prefix $V2_INVITE suffix"

printf 'dump sanitizer tests passed\n'
