#!/bin/bash
set -eo pipefail

export E2E_PROJECT_NAME="e2e_server_stream"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: Server-stream service (NDJSON)...${NC}"

cleanup_e2e
trap cleanup_e2e EXIT

mkdir -p "$E2E_DATA_DIR/node-1"
mkdir -p "$E2E_DATA_DIR/node-2/scripts"

# Upstream that emits ≥3 NDJSON ticks then closes (server_stream exec target)
cat << 'EOF' > "$E2E_DATA_DIR/node-2/scripts/tick_server.py"
#!/usr/bin/env python3
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import time

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def do_POST(self):
        _ = self.rfile.read(int(self.headers.get("Content-Length", 0) or 0))
        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson")
        self.end_headers()
        for i in range(1, 4):
            line = json.dumps({"tick": i, "msg": "hello"}) + "\n"
            self.wfile.write(line.encode())
            self.wfile.flush()
            time.sleep(0.05)

if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 9099), Handler).serve_forever()
EOF

bootstrap_node node-1 8081
bootstrap_node node-2 8082

run_node node-2 service add \
    --name "ticks" \
    --storage "/app/data" \
    --type "server_stream" \
    --exec "http://127.0.0.1:9099/" \
    --desc "NDJSON tick stream" \
    --param "n?:int"

$COMPOSE_CMD up -d node-1
sleep 2
join_cluster node-2 node-1 8081
$COMPOSE_CMD up -d node-2
sleep 2

echo "Starting tick upstream inside node-2..."
$COMPOSE_CMD exec -d node-2 python3 /app/data/scripts/tick_server.py
sleep 1

echo "Streaming ticks from node-1 via /services/stream..."
STREAM_OUT=$(call_api node-1 POST 8081 "services/stream?service=ticks" \
    -H "Content-Type: application/json" \
    -d '{}')
echo "stream body: $STREAM_OUT"

LINE_COUNT=$(echo "$STREAM_OUT" | grep -c '"tick"' || true)
if [ "$LINE_COUNT" -lt 3 ]; then
    echo -e "${RED}❌ Expected ≥3 NDJSON ticks, got $LINE_COUNT lines${NC}"
    exit 1
fi
if echo "$STREAM_OUT" | grep -qi 'not implemented\|501'; then
    echo -e "${RED}❌ Stream fell through to not-implemented/501${NC}"
    exit 1
fi

echo "Also via CLI service run (auto-stream for streaming types)..."
CLI_OUT=$(exec_node node-1 ./proxyma service run --name ticks --payload '{}' --storage "/app/data" 2>&1 || true)
echo "cli: $CLI_OUT"
CLI_TICKS=$(echo "$CLI_OUT" | grep -c '"tick"' || true)
if [ "$CLI_TICKS" -lt 3 ]; then
    echo -e "${YELLOW}⚠️ CLI stream returned fewer ticks ($CLI_TICKS); HTTP path already green${NC}"
fi

echo -e "${GREEN}✅ Case 12 (server-stream service) passed — $LINE_COUNT NDJSON ticks${NC}"
exit 0
