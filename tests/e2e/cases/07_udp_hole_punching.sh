#!/bin/bash
set -euo pipefail

# E2E project setup
export E2E_PROJECT_NAME="e2e_udp_hole_punching"
export E2E_DATA_DIR="/tmp/proxyma-e2e/$E2E_PROJECT_NAME"

# Load helpers
SCRIPTPATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPTPATH/../lib/helpers.sh"

echo -e "${GREEN}🚀 Starting test case: UDP NAT Hole Punching via STUN & QUIC...${NC}"

install_e2e_case_trap "case-07-failure"
cleanup_e2e

# Create directories
mkdir -p "$E2E_DATA_DIR/node-1/scripts"
mkdir -p "$E2E_DATA_DIR/node-2"
mkdir -p "$E2E_DATA_DIR/node-3"

# Write mock STUN server script
cat << 'EOF' > "$E2E_DATA_DIR/node-1/scripts/stun_server.py"
import socket

def run_stun():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(('0.0.0.0', 3478))
    with open("/app/data/stun.ready", "w", encoding="utf-8") as ready:
        ready.write("ready\n")
    print("STUN mock server listening on port 3478")
    while True:
        data, addr = sock.recvfrom(1024)
        if len(data) < 20:
            continue
        magic = data[4:8]
        if magic != b'\x21\x12\xa4\x42':
            continue
        tx_id = data[8:20]
        ip_bytes = socket.inet_aton(addr[0])
        port = addr[1]
        x_port = port ^ 0x2112
        x_port_bytes = x_port.to_bytes(2, 'big')
        ip_val = int.from_bytes(ip_bytes, 'big')
        x_ip = ip_val ^ 0x2112A442
        x_ip_bytes = x_ip.to_bytes(4, 'big')
        attr = b'\x00\x20\x00\x08\x00\x01' + x_port_bytes + x_ip_bytes
        header = b'\x01\x01\x00\x0c\x21\x12\xa4\x42' + tx_id
        sock.sendto(header + attr, addr)

if __name__ == '__main__':
    run_stun()
EOF

# Initialize nodes
# Node-1 is public Sponsor node
bootstrap_node node-1 8081

# Node-2 and Node-3 are behind simulated NATs:
# We bind their daemon listening addresses to loopback (127.0.0.1) so other nodes cannot reach them via TCP.
# They will use Node-1 as their STUN server to discover their public UDP/QUIC endpoints.
bootstrap_node node-2 8082
bootstrap_node node-3 8083

# Overwrite node configs to use local loopback listener for TCP (preventing direct TCP inter-node routing)
# and configure their STUN server pointing to node-1.
# Also override Sponsor settings.
cat << 'EOF' > "$E2E_DATA_DIR/node-2/config.json"
{
  "id": "node-2",
  "address": "https://127.0.0.1:8082",
  "storage_path": "/app/data",
  "stun_server": "node-1:3478",
  "min_relay_poll_interval": 1,
  "max_relay_poll_interval": 2
}
EOF

cat << 'EOF' > "$E2E_DATA_DIR/node-3/config.json"
{
  "id": "node-3",
  "address": "https://127.0.0.1:8083",
  "storage_path": "/app/data",
  "stun_server": "node-1:3478",
  "min_relay_poll_interval": 1,
  "max_relay_poll_interval": 2
}
EOF

# Bring up Node 1 (Sponsor)
start_node node-1 8081

# Start Python STUN Mock Server inside node-1 container
echo "Starting STUN server on node-1..."
exec_node node-1 python3 /app/data/scripts/stun_server.py > /dev/null 2>&1 &
wait_until 15 "mock STUN fixture readiness" \
    exec_node node-1 test -f /app/data/stun.ready >/dev/null

# Join Node 2 & 3 to the Sponsor
join_cluster node-2 node-1 8081
join_cluster node-3 node-1 8081

# Modify configs post-join in place to set the listener address to local loopback (127.0.0.1)
python3 -c "import json; p='$E2E_DATA_DIR/node-2/config.json'; d=json.load(open(p)); d['address']='https://127.0.0.1:8082'; json.dump(d, open(p, 'w'))"
python3 -c "import json; p='$E2E_DATA_DIR/node-3/config.json'; d=json.load(open(p)); d['address']='https://127.0.0.1:8083'; json.dump(d, open(p, 'w'))"

# Bring up node-3 first and let it announce presence to the Sponsor
start_node node-3 8083
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-3 \
    exec_node node-1 ./proxyma peers list --storage /app/data >/dev/null

# Bring up node-2
start_node node-2 8082
wait_for_output "${E2E_DISCOVERY_TIMEOUT:-45}" node-2 \
    exec_node node-1 ./proxyma peers list --storage /app/data >/dev/null

# Add file to node-3 VFS
echo "Direct UDP sync test payload" > "$E2E_DATA_DIR/node-3/test_udp.txt"
exec_node node-3 ./proxyma storage upload --name "test_udp.txt" --path "/app/data/test_udp.txt" --storage "/app/data"

# Force sync on node-3 to announce metadata to Sponsor
echo "Triggering VFS sync on node-3 to announce file..."
exec_node node-3 ./proxyma storage sync --storage "/app/data"

# Subscribe node-2 to the file
exec_node node-2 ./proxyma storage subscribe --name "test_udp.txt" --storage "/app/data"

# Trigger sync from node-2
echo "Triggering VFS sync on node-2..."
exec_node node-2 ./proxyma storage sync --storage "/app/data"

# Verify the public VFS outcome under the loopback-only TCP/STUN setup. The test
# deliberately does not infer or inspect a private transport selection.
echo "Checking if file is synced to node-2..."
wait_for_output "${E2E_VFS_TIMEOUT:-60}" test_udp.txt \
    exec_node node-2 ./proxyma storage list --storage "/app/data" >/dev/null

# Get file hash
MANIFEST_N2=$(call_api node-2 GET 8082 manifest)
FILE_HASH=$(echo "$MANIFEST_N2" | grep -o '"test_udp.txt":{"name":"test_udp.txt","size":[^,]*,"hash":"[^"]*"' | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)

if [ -z "$FILE_HASH" ]; then
    echo -e "${RED}❌ Error: Could not find the hash of test_udp.txt in the manifest${NC}"
    exit 1
fi

# Verify the file contents on node-2
FILE_CONTENT=$(call_api node-2 GET 8082 "download/$FILE_HASH")
if [ "$FILE_CONTENT" != "Direct UDP sync test payload" ]; then
    echo -e "${RED}❌ Synced file content mismatch! Got: '$FILE_CONTENT'${NC}"
    exit 1
fi

echo -e "${GREEN}✅ VFS file synchronized under the simulated NAT configuration.${NC}"
echo -e "${GREEN}🎉 Case 7 (public NAT sync outcome) completed successfully!${NC}"
exit 0
