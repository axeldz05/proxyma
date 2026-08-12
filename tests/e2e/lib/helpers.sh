#!/bin/bash

# Compatibility facade. Existing cases keep sourcing helpers.sh while new cases
# can source the focused modules directly.
E2E_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=cluster.sh
source "$E2E_LIB_DIR/cluster.sh"
# shellcheck source=wait.sh
source "$E2E_LIB_DIR/wait.sh"
# shellcheck source=assert.sh
source "$E2E_LIB_DIR/assert.sh"
# shellcheck source=faults.sh
source "$E2E_LIB_DIR/faults.sh"
# shellcheck source=dump.sh
source "$E2E_LIB_DIR/dump.sh"
# shellcheck source=case.sh
source "$E2E_LIB_DIR/case.sh"
