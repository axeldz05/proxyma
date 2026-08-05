#!/usr/bin/env bash

set -e

STORAGE_DIR="${PROXYMA_DEV_DIR:-$HOME/.proxyma_dev}"
mkdir -p "$STORAGE_DIR/scripts"

# Clean up any existing instances
echo "Stopping any running proxyma instance..."
pkill -9 -f "proxyma run" || true
sleep 1

# Compile proxyma CLI if not done
echo "Building proxyma binary..."
go build -o "$STORAGE_DIR/proxyma" ./cmd/proxyma

SERVICES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/services-examples"

# Sync Python dependencies with uv if available
if command -v uv >/dev/null 2>&1; then
    echo "Syncing Python environment in services-examples using uv..."
    uv sync --project "$SERVICES_DIR" || true
fi

# Compile editor binary
echo "Building editor binary..."
go -C "$SERVICES_DIR/editor" build -o proxyma-editor .

# Compile collab_editor binary
echo "Building collab_editor binary..."
go -C "$SERVICES_DIR/collab_editor" build -o proxyma-collab .

# Start the daemon
echo "Starting Proxyma daemon on port 8080..."
setsid nohup "$STORAGE_DIR/proxyma" run --storage "$STORAGE_DIR" >> "$STORAGE_DIR/proxyma.log" 2>&1 < /dev/null &
DAEMON_PID=$!
disown -h $DAEMON_PID 2>/dev/null || true

# Wait for socket
echo "Waiting for Unix socket at $STORAGE_DIR/proxyma.sock..."
for i in {1..40}; do
    if [ -S "$STORAGE_DIR/proxyma.sock" ]; then
        sleep 2
        break
    fi
    sleep 0.5
done

if [ ! -S "$STORAGE_DIR/proxyma.sock" ]; then
    echo "Error: Daemon socket did not appear."
    exit 1
fi

# Register services from services-examples
echo "Registering services..."
"$STORAGE_DIR/proxyma" service add --name "$SERVICES_DIR/ocr/ocr_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$SERVICES_DIR/extract/extract_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$SERVICES_DIR/obsidian/obsidian_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$SERVICES_DIR/editor/editor_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$SERVICES_DIR/collab_editor/collab_editor_service.json" --storage "$STORAGE_DIR"

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
