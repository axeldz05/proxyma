#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEV_DIR="$(mktemp -d "${TMPDIR:-/tmp}/proxyma-bootstrap-smoke.XXXXXX")"

cleanup() {
    if [[ -f "$DEV_DIR/proxyma.pid" ]]; then
        daemon_pid="$(<"$DEV_DIR/proxyma.pid")"
        kill -TERM "$daemon_pid" 2>/dev/null || true
        for _ in {1..40}; do
            if ! kill -0 "$daemon_pid" 2>/dev/null; then
                break
            fi
            sleep 0.1
        done
        kill -KILL "$daemon_pid" 2>/dev/null || true
    fi
    rm -rf "$DEV_DIR"
}
trap cleanup EXIT

export PROXYMA_DEV_DIR="$DEV_DIR"
export PROXYMA_BOOTSTRAP_SKIP_KILL=1
export PROXYMA_BOOTSTRAP_SKIP_UV=1
export PROXYMA_BOOTSTRAP_SKIP_EXTRA_BUILDS=1
export PROXYMA_BOOTSTRAP_SKIP_FIXTURES=1
export PROXYMA_BOOTSTRAP_SKIP_UPSTREAMS=1
export PROXYMA_BOOTSTRAP_SKIP_SAMPLES=1
export PROXYMA_BOOTSTRAP_INIT_PORT=0
export PROXYMA_BOOTSTRAP_NODE_ID=bootstrap-smoke

bash "$REPO_ROOT/scripts/bootstrap_dev.sh"

[[ -S "$DEV_DIR/proxyma.sock" ]] || {
    echo "bootstrap smoke: daemon socket missing"
    test ! -f "$DEV_DIR/proxyma.log" || tail -n 100 "$DEV_DIR/proxyma.log"
    exit 1
}
[[ -x "$DEV_DIR/pm" ]] || {
    echo "bootstrap smoke: pm wrapper missing"
    exit 1
}

storage_output="$("$DEV_DIR/pm" storage list)"
[[ "$storage_output" == *"NAME"* || "$storage_output" == *"No records"* ]] || {
    echo "bootstrap smoke: storage list was not rendered"
    echo "$storage_output"
    exit 1
}

services_output="$("$DEV_DIR/pm" service discover)"
[[ "$services_output" == *"sensor.telemetry"* ]] || {
    echo "bootstrap smoke: expected service was not registered"
    echo "$services_output"
    exit 1
}

pipelines_output="$("$DEV_DIR/pm" service list_pipelines)"
[[ "$pipelines_output" == *"media-thumbnail-pipeline"* ]] || {
    echo "bootstrap smoke: expected pipeline was not registered"
    echo "$pipelines_output"
    exit 1
}

echo "bootstrap_dev smoke contract passed"
