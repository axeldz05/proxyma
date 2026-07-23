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

# Compile editor binary
echo "Building editor binary..."
go -C /home/drusila/Projects/proxyma-services/editor build -o proxyma-editor .

# Start the daemon
echo "Starting Proxyma daemon on port 8080..."
setsid nohup "$STORAGE_DIR/proxyma" run --storage "$STORAGE_DIR" >> "$STORAGE_DIR/proxyma.log" 2>&1 < /dev/null &
DAEMON_PID=$!
disown -h $DAEMON_PID 2>/dev/null || true

# Wait for socket
echo "Waiting for Unix socket at $STORAGE_DIR/proxyma.sock..."
for i in {1..20}; do
    if [ -S "$STORAGE_DIR/proxyma.sock" ]; then
        break
    fi
    sleep 0.5
done

if [ ! -S "$STORAGE_DIR/proxyma.sock" ]; then
    echo "Error: Daemon socket did not appear."
    exit 1
fi

# Create dynamic service JSON definitions
cat << 'EOF' > "$STORAGE_DIR/scripts/ocr_service.json"
{
    "type": "script",
    "exec": "/home/drusila/Projects/proxyma-services/.venv/bin/python /home/drusila/Projects/proxyma-services/ocr_service.py",
    "schema": {
        "name": "ocr",
        "description": "OCR service to optimize PDF files",
        "parameters": {
            "input_path": {"type": "string", "required": true},
            "lang": {"type": "string", "required": false},
            "force_ocr": {"type": "bool", "required": false}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "output_path": {"type": "string"}
        }
    }
}
EOF

cat << 'EOF' > "$STORAGE_DIR/scripts/extract_service.json"
{
    "type": "script",
    "exec": "/home/drusila/Projects/proxyma-services/.venv/bin/python /home/drusila/Projects/proxyma-services/extract_service.py",
    "schema": {
        "name": "text/extract",
        "description": "Extract text from PDF or Image",
        "parameters": {
            "input_path": {"type": "string", "required": true}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "text": {"type": "string"}
        }
    }
}
EOF

cat << 'EOF' > "$STORAGE_DIR/scripts/obsidian_service.json"
{
    "type": "script",
    "exec": "/home/drusila/Projects/proxyma-services/.venv/bin/python /home/drusila/Projects/proxyma-services/obsidian_service.py",
    "schema": {
        "name": "obsidian/save",
        "description": "Save text to Obsidian note",
        "parameters": {
            "text": {"type": "string", "required": true},
            "vault_path": {
                "type": "string",
                "required": false,
                "default": "/home/drusila/ObsidianVaultCollection/Knowledge",
                "options": [
                    "/home/drusila/ObsidianVaultCollection/Knowledge",
                    "/home/drusila/ObsidianVaultCollection/TORUniverse"
                ]
            },
            "note_name": {"type": "string", "required": false}
        },
        "outputs": {
            "status": {"type": "string"},
            "message": {"type": "string"},
            "note_path": {"type": "string"}
        }
    }
}
EOF

cat << 'EOF' > "$STORAGE_DIR/scripts/editor_service.json"
{
    "type": "exec",
    "exec": "/home/drusila/Projects/proxyma-services/editor/proxyma-editor",
    "schema": {
        "name": "pipeline/editor",
        "description": "Interactive TUI Pipeline Schema Editor",
        "parameters": {
            "storage": {"type": "string", "required": false}
        },
        "outputs": {
            "status": {"type": "string"}
        }
    }
}
EOF

# Register services
echo "Registering services..."
"$STORAGE_DIR/proxyma" service add --name "$STORAGE_DIR/scripts/ocr_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$STORAGE_DIR/scripts/extract_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$STORAGE_DIR/scripts/obsidian_service.json" --storage "$STORAGE_DIR"
"$STORAGE_DIR/proxyma" service add --name "$STORAGE_DIR/scripts/editor_service.json" --storage "$STORAGE_DIR"

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
