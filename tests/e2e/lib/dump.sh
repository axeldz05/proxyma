#!/bin/bash

# Redact credentials before diagnostics reach console logs or artifacts.

e2e_sanitize() {
    awk '
        /-----BEGIN .*PRIVATE KEY-----/ {
            print "-----BEGIN PRIVATE KEY-----"
            in_private_key = 1
            next
        }
        /-----END .*PRIVATE KEY-----/ {
            print "-----END PRIVATE KEY-----"
            in_private_key = 0
            next
        }
        in_private_key { next }
        {
            line = $0
            gsub(/[Bb]earer[[:space:]]+[A-Za-z0-9._~+\/=-]+/, "Bearer [REDACTED]", line)
            gsub(/--token(=|[[:space:]]+)[^[:space:]]+/, "--token [REDACTED]", line)
            gsub(/"token"[[:space:]]*:[[:space:]]*"[^"]*"/, "\"token\":\"[REDACTED]\"", line)
            gsub(/token=[^&[:space:]]+/, "token=[REDACTED]", line)
            print line
        }
    '
}

sanitize_file() {
    local path=$1
    local temporary="${path}.sanitized.$$"

    [ -f "$path" ] || return 0
    if e2e_sanitize <"$path" >"$temporary"; then
        mv "$temporary" "$path"
    else
        rm -f "$temporary"
        return 1
    fi
}

dump_e2e_diagnostics() {
    local reason=${1:-failure}
    local root=${E2E_DIAGNOSTICS_DIR:-"${E2E_ROOT:-$(pwd)}/logs/diagnostics/${E2E_PROJECT_NAME:-unknown}"}
    local timestamp destination

    timestamp=$(date -u +%Y%m%dT%H%M%SZ)
    destination="$root/$timestamp"
    mkdir -p "$destination"

    {
        echo "project=${E2E_PROJECT_NAME:-unknown}"
        echo "reason=$reason"
        echo "captured_at=$timestamp"
    } >"$destination/summary.txt"

    if declare -F e2e_compose >/dev/null; then
        e2e_compose ps --all 2>&1 | e2e_sanitize >"$destination/compose-ps.txt" || true
        e2e_compose logs --no-color --timestamps \
            --tail "${E2E_DIAGNOSTIC_LOG_LINES:-200}" 2>&1 |
            e2e_sanitize >"$destination/compose-logs.txt" || true

        {
            local node_id port
            for node_id in node-1 node-2 node-3 node-low-spec; do
                if [ -n "$(e2e_container_id "$node_id" 2>/dev/null)" ]; then
                    port=$(node_port "$node_id")
                    echo "[$node_id GET /telemetry]"
                    call_api "$node_id" GET "$port" telemetry 2>&1 || true
                fi
            done
        } | e2e_sanitize >"$destination/public-health.txt" || true
    fi

    echo "Sanitized diagnostics: $destination" >&2
}
