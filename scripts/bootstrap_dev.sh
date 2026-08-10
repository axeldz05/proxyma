#!/usr/bin/env bash

set -e

STORAGE_DIR="${PROXYMA_DEV_DIR:-$HOME/.proxyma_dev}"
mkdir -p "$STORAGE_DIR/scripts"

# Clean up any existing instances
echo "Stopping any running proxyma instance..."
pkill -9 -f "proxyma run" || true
sleep 1

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICES_DIR="$REPO_ROOT/services-examples"

# Compile proxyma CLI if not done
echo "Building proxyma binary..."
go build -C "$REPO_ROOT" -o "$STORAGE_DIR/proxyma" ./cmd/proxyma

# Sync Python dependencies with uv if available
if command -v uv >/dev/null 2>&1; then
    echo "Syncing Python environment in services-examples using uv..."
    uv sync --project "$SERVICES_DIR" || true
fi

# Compile editor binary (part of main proxyma module)
echo "Building editor binary..."
go build -C "$REPO_ROOT" -o "$SERVICES_DIR/editor/proxyma-editor" ./services-examples/editor

# Compile collab_editor binary
echo "Building collab_editor binary..."
go -C "$SERVICES_DIR/collab_editor" build -o proxyma-collab .

# Start the daemon
echo "Starting Proxyma daemon on port 8080..."
setsid nohup "$STORAGE_DIR/proxyma" run --storage "$STORAGE_DIR" >> "$STORAGE_DIR/proxyma.log" 2>&1 < /dev/null &
DAEMON_PID=$!
disown -h $DAEMON_PID 2>/dev/null || true

# Wait for socket (basename must match protocol.SockFileName)
SOCK_PATH="$STORAGE_DIR/proxyma.sock"
echo "Waiting for Unix socket at $SOCK_PATH..."
for i in {1..40}; do
    if [ -S "$SOCK_PATH" ]; then
        sleep 2
        break
    fi
    sleep 0.5
done

if [ ! -S "$SOCK_PATH" ]; then
    echo "Error: Daemon socket did not appear."
    exit 1
fi

# Ensure music fixtures exist
if [[ ! -d "$SERVICES_DIR/music/fixtures/library/demo-hi" ]]; then
    echo "Generating music fixtures..."
    python3 "$SERVICES_DIR/music/fixtures/gen_fixtures.py" || true
fi

# HTTP upstreams for server_stream examples
echo "Starting example stream upstreams..."
bash "$SERVICES_DIR/start_upstreams.sh" || true

rewrite_placeholders() {
    local src="$1"
    local dest="$2"
    sed "s|__SERVICES_DIR__|${SERVICES_DIR}|g" "$src" > "$dest"
}

# Register all lab services (glob + placeholder rewrite)
echo "Registering services..."
TMP_SVC_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_SVC_DIR"' EXIT
while IFS= read -r -d '' f; do
    base="$(basename "$f")"
    rewritten="$TMP_SVC_DIR/$base"
    rewrite_placeholders "$f" "$rewritten"
    echo "  + $base"
    "$STORAGE_DIR/proxyma" service add --name "$rewritten" --storage "$STORAGE_DIR" || true
done < <(find "$SERVICES_DIR" -name '*_service.json' -print0 | sort -z)

# Register all lab pipelines (includes ocr_obsidian_pipeline.json)
echo "Registering example pipelines..."
while IFS= read -r -d '' f; do
    id="$(python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get('id') or '')" "$f")"
    if [[ -z "$id" ]]; then
        id="$(basename "$f" .json)"
    fi
    echo "  + $id ($f)"
    "$STORAGE_DIR/proxyma" service add_pipeline --id "$id" --schema-file "$f" --storage "$STORAGE_DIR" || true
done < <(find "$SERVICES_DIR" -name '*_pipeline.json' -print0 | sort -z)

# Pre-populate random files
echo "Pre-populating VFS with sample files..."
echo "Draft proposal for P2P Compute network design" > "/tmp/sample_proposal.txt"
"$STORAGE_DIR/proxyma" storage upload --name "proposal.txt" --path "/tmp/sample_proposal.txt" --storage "$STORAGE_DIR"

echo "Invoice ID: 948271 - Date: 2026-07-20" > "/tmp/sample_invoice.txt"
"$STORAGE_DIR/proxyma" storage upload --name "invoice.txt" --path "/tmp/sample_invoice.txt" --storage "$STORAGE_DIR"

rm -f "/tmp/sample_proposal.txt" "/tmp/sample_invoice.txt"

# Create a developer command wrapper 'pm' so the user doesn't have to specify --storage every time
cat << EOF > "$STORAGE_DIR/pm"
#!/bin/bash
STORAGE_DIR="$STORAGE_DIR"
"\$STORAGE_DIR/proxyma" "\$@" --storage "\$STORAGE_DIR"
EOF
chmod +x "$STORAGE_DIR/pm"

echo "===================================================="
echo "🎉 Node bootstrapped successfully!"
echo "Daemon running with PID: $DAEMON_PID"
echo "Storage directory: $STORAGE_DIR"
echo "Log file: $STORAGE_DIR/proxyma.log"
echo "===================================================="
echo "To interact with this node easily, use the helper wrapper:"
echo "  $STORAGE_DIR/pm storage list"
echo "  $STORAGE_DIR/pm service discover"
echo ""
echo "To launch the visual pipeline editor, run:"
echo "  $STORAGE_DIR/pm service edit_pipeline"
echo "===================================================="
