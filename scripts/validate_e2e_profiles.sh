#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CASES_DIR=${1:-"$REPO_ROOT/tests/e2e/cases"}
PROFILES_DIR=${2:-"$REPO_ROOT/tests/e2e/profiles"}

declare -a REQUIRED_PROFILES=(full quarantine pr functional network infrastructure smoke)
declare -a CASE_NAMES=()
declare -A PROJECT_CASES=()
declare -A PROFILE_MEMBERS=()
errors=0

report_error() {
    printf 'profile validation: %s\n' "$*" >&2
    ((errors += 1))
}

trim() {
    local value=$1
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    printf '%s\n' "$value"
}

if [ ! -d "$CASES_DIR" ]; then
    printf 'profile validation: missing cases directory: %s\n' "$CASES_DIR" >&2
    exit 1
fi
if [ ! -d "$PROFILES_DIR" ]; then
    printf 'profile validation: missing profiles directory: %s\n' "$PROFILES_DIR" >&2
    exit 1
fi

shopt -s nullglob
case_files=("$CASES_DIR"/*.sh)
if [ ${#case_files[@]} -eq 0 ]; then
    report_error "no case scripts found in $CASES_DIR"
fi

project_pattern='^[[:space:]]*export[[:space:]]+E2E_PROJECT_NAME="([a-z0-9][a-z0-9_-]*)"[[:space:]]*$'
for case_file in "${case_files[@]}"; do
    case_name=$(basename "$case_file" .sh)
    CASE_NAMES+=("$case_name")

    if [ ! -x "$case_file" ]; then
        report_error "case is not executable: $case_name"
    fi

    project_name=""
    project_declarations=0
    while IFS= read -r line || [ -n "$line" ]; do
        if [[ $line =~ $project_pattern ]]; then
            project_name=${BASH_REMATCH[1]}
            ((project_declarations += 1))
        fi
    done <"$case_file"

    if [ "$project_declarations" -ne 1 ]; then
        report_error "$case_name must declare one static E2E_PROJECT_NAME (found $project_declarations)"
        continue
    fi
    if [ -n "${PROJECT_CASES[$project_name]:-}" ]; then
        report_error "duplicate E2E_PROJECT_NAME '$project_name': ${PROJECT_CASES[$project_name]} and $case_name"
    else
        PROJECT_CASES["$project_name"]=$case_name
    fi
done

for profile in "${REQUIRED_PROFILES[@]}"; do
    if [ ! -f "$PROFILES_DIR/$profile.txt" ]; then
        report_error "missing required profile file: $profile.txt"
    fi
done

profile_files=("$PROFILES_DIR"/*.txt)
for profile_file in "${profile_files[@]}"; do
    profile=$(basename "$profile_file" .txt)
    line_number=0
    while IFS= read -r profile_line || [ -n "$profile_line" ]; do
        ((line_number += 1))
        profile_line=${profile_line%%#*}
        selector=$(trim "$profile_line")
        [ -n "$selector" ] || continue

        matches=()
        for case_name in "${CASE_NAMES[@]}"; do
            if [[ "$case_name" == "$selector" || "$case_name.sh" == "$selector" ||
                "$case_name" == "$selector"_* ]]; then
                matches+=("$case_name")
            fi
        done

        if [ ${#matches[@]} -eq 0 ]; then
            report_error "$profile.txt:$line_number selector '$selector' matches no case file"
            continue
        fi
        if [ ${#matches[@]} -gt 1 ]; then
            report_error "$profile.txt:$line_number selector '$selector' is ambiguous (${matches[*]})"
            continue
        fi

        case_name=${matches[0]}
        member_key="$profile|$case_name"
        if [ -n "${PROFILE_MEMBERS[$member_key]:-}" ]; then
            report_error "$profile.txt:$line_number duplicates case '$case_name'"
            continue
        fi
        PROFILE_MEMBERS["$member_key"]=1
    done <"$profile_file"
done

for case_name in "${CASE_NAMES[@]}"; do
    in_full=${PROFILE_MEMBERS["full|$case_name"]:-}
    in_quarantine=${PROFILE_MEMBERS["quarantine|$case_name"]:-}
    if [ -z "$in_full" ] && [ -z "$in_quarantine" ]; then
        report_error "orphan case is in neither full nor quarantine: $case_name"
    fi
    if [ -n "$in_full" ] && [ -n "$in_quarantine" ]; then
        report_error "case overlaps full and quarantine: $case_name"
    fi
done

for case_name in "${CASE_NAMES[@]}"; do
    in_pr=${PROFILE_MEMBERS["pr|$case_name"]:-}
    in_functional=${PROFILE_MEMBERS["functional|$case_name"]:-}
    if [ -n "$in_pr" ] && [ -z "$in_functional" ]; then
        report_error "pr/functional drift: $case_name is only in pr"
    elif [ -z "$in_pr" ] && [ -n "$in_functional" ]; then
        report_error "pr/functional drift: $case_name is only in functional"
    fi
done

if [ "$errors" -ne 0 ]; then
    printf 'profile validation failed with %d error(s)\n' "$errors" >&2
    exit 1
fi

printf 'validated %d E2E cases across %d profiles\n' \
    "${#CASE_NAMES[@]}" "${#profile_files[@]}"
