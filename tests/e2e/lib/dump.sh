#!/bin/bash

# Redact credentials before diagnostics reach console logs or artifacts.

e2e_decode_base64url() {
    local value=$1
    local padding=""

    case $((${#value} % 4)) in
        0) ;;
        2) padding="==" ;;
        3) padding="=" ;;
        *) return 1 ;;
    esac

    printf '%s%s' "$value" "$padding" | tr '_-' '/+' | base64 --decode
}

e2e_is_v1_invite_token() {
    local token=$1
    local encoded_payload payload
    local address_pattern='"address"[[:space:]]*:[[:space:]]*"[^"]+"'
    local ca_hash_pattern='"ca_hash"[[:space:]]*:[[:space:]]*"[[:xdigit:]]{64}"'

    ((${#token} <= 8192)) || return 1
    [[ $token =~ ^([A-Za-z0-9_-]+)\.([[:xdigit:]]{64})$ ]] || return 1
    encoded_payload=${BASH_REMATCH[1]}
    payload=$(e2e_decode_base64url "$encoded_payload" 2>/dev/null) || return 1

    [[ $payload == \{*\} ]] || return 1
    [[ $payload =~ $address_pattern ]] || return 1
    [[ $payload =~ $ca_hash_pattern ]]
}

e2e_is_v2_invite_token() {
    local token=$1
    local byte_dump entry_type field_length
    local -a bytes
    local index=68
    local entry

    ((${#token} >= 91 && ${#token} <= 8192)) || return 1
    [[ $token =~ ^A[A-Za-z0-9_-]+$ ]] || return 1
    byte_dump=$(e2e_decode_base64url "$token" 2>/dev/null | od -An -v -tu1) || return 1
    byte_dump=${byte_dump//$'\n'/ }
    read -r -a bytes <<<"$byte_dump"

    ((${#bytes[@]} >= 68)) || return 1
    ((${bytes[0]} == 2)) || return 1

    for ((entry = 0; entry < bytes[67]; entry++)); do
        ((index < ${#bytes[@]})) || return 1
        entry_type=${bytes[index]}
        ((index += 1))
        case $entry_type in
            1)
                ((index + 4 <= ${#bytes[@]})) || return 1
                ((index += 4))
                ;;
            2)
                ((index + 16 <= ${#bytes[@]})) || return 1
                ((index += 16))
                ;;
            3)
                ((index < ${#bytes[@]})) || return 1
                field_length=${bytes[index]}
                ((index += 1))
                ((index + field_length <= ${#bytes[@]})) || return 1
                ((index += field_length))
                ;;
            4)
                ((index < ${#bytes[@]})) || return 1
                field_length=${bytes[index]}
                ((index += 1))
                ((index + field_length < ${#bytes[@]})) || return 1
                ((index += field_length))
                field_length=${bytes[index]}
                ((index += 1))
                ((index + field_length <= ${#bytes[@]})) || return 1
                ((index += field_length))
                ;;
            *)
                return 1
                ;;
        esac
    done

    ((index == ${#bytes[@]}))
}

e2e_redact_invite_lines() {
    local line candidate

    while IFS= read -r line || [ -n "$line" ]; do
        candidate=${line#"${line%%[![:space:]]*}"}
        candidate=${candidate%"${candidate##*[![:space:]]}"}
        if ((${#candidate} >= 2)); then
            if [[ $candidate == \"*\" && $candidate == *\" ]] ||
                [[ $candidate == \'*\' && $candidate == *\' ]]; then
                candidate=${candidate:1:${#candidate}-2}
            fi
        fi

        if e2e_is_v1_invite_token "$candidate" || e2e_is_v2_invite_token "$candidate"; then
            printf '%s\n' '[REDACTED INVITE TOKEN]'
        else
            printf '%s\n' "$line"
        fi
    done
}

e2e_sanitize() {
    e2e_redact_invite_lines | awk '
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
            gsub(/"secret"[[:space:]]*:[[:space:]]*"[^"]*"/, "\"secret\":\"[REDACTED]\"", line)
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
