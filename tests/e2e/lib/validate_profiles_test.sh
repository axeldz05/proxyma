#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
VALIDATOR="$REPO_ROOT/scripts/validate_e2e_profiles.sh"
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/proxyma-profile-validator.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT

make_case() {
    local root=$1
    local case_name=$2
    local project_name=$3

    cat >"$root/cases/$case_name.sh" <<EOF
#!/bin/bash
set -euo pipefail
export E2E_PROJECT_NAME="$project_name"
EOF
    chmod +x "$root/cases/$case_name.sh"
}

make_fixture() {
    local root=$1

    mkdir -p "$root/cases" "$root/profiles"
    make_case "$root" 01_alpha e2e_alpha
    make_case "$root" 02_beta e2e_beta
    make_case "$root" 03_gamma e2e_gamma

    printf '%s\n' 01_alpha 02_beta >"$root/profiles/full.txt"
    printf '%s\n' 03_gamma >"$root/profiles/quarantine.txt"
    printf '%s\n' 01_alpha >"$root/profiles/pr.txt"
    printf '%s\n' 01_alpha >"$root/profiles/functional.txt"
    printf '%s\n' 02_beta >"$root/profiles/network.txt"
    printf '%s\n' 01_alpha >"$root/profiles/infrastructure.txt"
    printf '%s\n' 02_beta >"$root/profiles/smoke.txt"
}

expect_failure() {
    local name=$1
    local expected=$2
    local root="$TEST_ROOT/$name"
    local output

    make_fixture "$root"
    shift 2
    "$@" "$root"
    if output=$(bash "$VALIDATOR" "$root/cases" "$root/profiles" 2>&1); then
        printf 'expected validator failure for %s\n' "$name" >&2
        return 1
    fi
    if [[ "$output" != *"$expected"* ]]; then
        printf 'validator failure for %s did not contain %q:\n%s\n' \
            "$name" "$expected" "$output" >&2
        return 1
    fi
}

add_missing_selector() {
    printf '%s\n' 99_missing >>"$1/profiles/network.txt"
}

add_duplicate_selector() {
    printf '%s\n' 02_beta >>"$1/profiles/network.txt"
}

add_orphan_case() {
    make_case "$1" 04_orphan e2e_orphan
}

add_overlap() {
    printf '%s\n' 03_gamma >>"$1/profiles/full.txt"
}

add_profile_drift() {
    printf '%s\n' 02_beta >>"$1/profiles/pr.txt"
}

remove_profile() {
    rm "$1/profiles/smoke.txt"
}

remove_execute_bit() {
    chmod -x "$1/cases/02_beta.sh"
}

add_duplicate_project() {
    make_case "$1" 04_delta e2e_gamma
    printf '%s\n' 04_delta >>"$1/profiles/quarantine.txt"
}

valid_root="$TEST_ROOT/valid"
make_fixture "$valid_root"
bash "$VALIDATOR" "$valid_root/cases" "$valid_root/profiles" >/dev/null

expect_failure missing-selector "matches no case file" add_missing_selector
expect_failure duplicate-selector "duplicates case" add_duplicate_selector
expect_failure orphan "orphan case" add_orphan_case
expect_failure overlap "overlaps full and quarantine" add_overlap
expect_failure profile-drift "pr/functional drift" add_profile_drift
expect_failure missing-profile "missing required profile file" remove_profile
expect_failure non-executable "case is not executable" remove_execute_bit
expect_failure duplicate-project "duplicate E2E_PROJECT_NAME" add_duplicate_project

printf 'profile validator tests passed\n'
