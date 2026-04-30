#!/bin/bash
# Enable strict mode: exit on error and fail on broken pipes
set -eo pipefail
echo "HOST_UID=$(id -u)" > .env
echo "HOST_GID=$(id -g)" >> .env

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

echo "🚀 Starting Proxyma cluster E2E test..."

echo "🧹 Cleaning up environment..."
docker compose down -v
rm -fr ./deploy proxyma_cli downloaded_test.txt test_e2e.txt || true
mkdir -p ./deploy/data/node-1 ./deploy/data/node-2 ./deploy/data/node-3

docker compose build
docker compose run --rm node-1 init --id "node-1" --port "8081" --storage "/app/data"
docker compose run --rm node-2 init --id "node-2" --port "8082" --storage "/app/data"
docker compose run --rm node-3 init --id "node-3" --port "8083" --storage "/app/data"
docker compose up -d node-1
sleep 3


echo "🎟️ [NODE-1]: Generating invite token for node 2..."
INVITE_OUTPUT=$(docker compose exec node-1 ./proxyma invite)
TOKEN=$(echo "$INVITE_OUTPUT" | grep -o "ey[a-zA-Z0-9._-]*")

echo "🔗 [NODE-2]: Joining the cluster..."
docker compose run --rm node-2 join --id "node-2" --token "$TOKEN"

echo "🎟️ [NODE-1]: Generating invite token for node 3..."
INVITE_OUTPUT=$(docker compose exec node-1 ./proxyma invite)
TOKEN=$(echo "$INVITE_OUTPUT" | grep -o "ey[a-zA-Z0-9._-]*")

echo "🔗 [NODE-3]: Joining the cluster..."
docker compose run --rm node-3 join --id "node-3" --token "$TOKEN"

docker compose up -d node-2 node-3
sleep 3


echo "📤 [HOST]: Uploading a file to node-1 via HTTP..."
echo "Hello Proxyma Cluster!" > test_e2e.txt
curl -s --cacert ./deploy/data/node-1/certs/ca.crt \
        --cert ./deploy/data/node-1/certs/node-1.crt \
        --key ./deploy/data/node-1/certs/node-1.key \
        -X POST -F "file=@test_e2e.txt" https://localhost:8081/upload

echo "🔄 [NODE-3]: Triggering manual sync from within the node..."
docker compose exec node-3 ./proxyma sync

echo "🔍 Verifying metadata replication on node-3..."
MAX_RETRIES=10
RETRY_COUNT=0
FILE_FOUND=false
MANIFEST_RESPONSE=""

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    MANIFEST_RESPONSE=$(curl -s --cacert ./deploy/data/node-3/certs/ca.crt \
        --cert ./deploy/data/node-3/certs/node-3.crt \
        --key ./deploy/data/node-3/certs/node-3.key \
        https://localhost:8083/manifest || echo "failed")
    
    if echo "$MANIFEST_RESPONSE" | grep -q "test_e2e.txt"; then
        FILE_FOUND=true
        break
    fi
    echo "   ... VFS not updated yet. Retrying ($RETRY_COUNT/$MAX_RETRIES)..."
    sleep 2
    RETRY_COUNT=$((RETRY_COUNT+1))
done

if [ "$FILE_FOUND" != true ]; then
    echo -e "${RED}❌ The file never reached node-3's VFS.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ VFS synchronized correctly.${NC}"

FILE_HASH=$(echo "$MANIFEST_RESPONSE" | grep -o '"hash":"[^"]*"' | cut -d'"' -f4)
echo "🏷️ File hash detected in the network: $FILE_HASH"

echo "🛡️ Verifying that the blob was NOT downloaded physically (No subscription)..."
if [ -f "./deploy/data/node-3/$FILE_HASH" ]; then
    echo -e "${RED}❌ SECURITY/LOGIC ERROR: Node 3 downloaded the file without being subscribed.${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Node 3 ignored the physical download, as expected.${NC}"

echo "📝 Subscribing node-3 to the file 'test_e2e.txt'..."
SUBSCRIBE_RESPONSE=$(curl -s --cacert ./deploy/data/node-3/certs/ca.crt \
        --cert ./deploy/data/node-3/certs/node-3.crt \
        --key ./deploy/data/node-3/certs/node-3.key \
  -X POST "https://localhost:8083/subscribe?name=test_e2e.txt")
echo "🔍 Server response: $SUBSCRIBE_RESPONSE"

if [[ -z "$SUBSCRIBE_RESPONSE" || "$SUBSCRIBE_RESPONSE" == *"error"* ]]; then
    echo -e "${RED}❌ Subscription failed!${NC}"
    exit 1
fi
echo -e "${GREEN}✅ Subscription successful.${NC}"

echo "🔄 Forcing a second Sync to trigger the P2P download..."
docker compose exec node-3 ./proxyma sync
echo "⬇️ Downloading the file from node-3's API..."
# 3. Descarga usando GET (sin -X POST) y extrayendo el código HTTP para depurar
HTTP_CODE=$(curl -s -w "%{http_code}" --cacert ./deploy/data/node-3/certs/ca.crt \
    --cert ./deploy/data/node-3/certs/node-3.crt \
    --key ./deploy/data/node-3/certs/node-3.key \
    "https://localhost:8083/download/$FILE_HASH" -o downloaded_test.txt)

if [ "$HTTP_CODE" != "200" ]; then
    echo -e "${RED}❌ Download failed with HTTP $HTTP_CODE${NC}"
    echo "🔍 Server said:"
    cat downloaded_test.txt
    exit 1
fi

echo "⚖️ Verifying cryptographic integrity of the content..."
if diff test_e2e.txt downloaded_test.txt > /dev/null; then
    echo -e "${GREEN}🎉 E2E Test Completed! The downloaded file from node-3 is bit-by-bit identical to the original.${NC}"
else
    echo -e "${RED}❌ Data corruption. The downloaded file does not match the original.${NC}"
    exit 1
fi

echo "🧹 Cleaning up environment..."
docker compose down -v
rm -fr ./deploy proxyma_cli downloaded_test.txt test_e2e.txt || true
