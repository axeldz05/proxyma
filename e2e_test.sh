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
rm -fr ./deploy
mkdir -p ./deploy/certs ./deploy/data/node-1 ./deploy/data/node-2 ./deploy/data/node-3


echo "🏗️ Spinning up containers (Build & Up)..."
docker compose up --build -d

echo "🔨 Compiling local CLI of Proxyma..."
go build -o proxyma_cli ./main.go

cleanup() {
    echo "🛑 Shutting down containers..."
    docker compose down -v
    echo "deleting generated files and binaries"
    rm -fr ./deploy
    rm ./proxyma_cli
    rm .env
}
trap cleanup EXIT

echo "⏳ Waiting for the cluster to come online..."
sleep 5

echo "📤 Uploading test file to node-1 (Port 8081)..."

echo "Hello Proxyma Cluster!" > test_e2e.txt

UPLOAD_RESPONSE=$(curl -s \
  --cacert ./deploy/certs/ca.crt \
  --cert ./deploy/certs/node-1.crt \
  --key ./deploy/certs/node-1.key \
  -X POST \
  -F "file=@test_e2e.txt" \
  https://localhost:8081/upload)

if echo "$UPLOAD_RESPONSE" | grep -q "successfully"; then
    echo -e "${GREEN}✅ File successfully uploaded to node-1.${NC}"
else
    echo -e "${RED}❌ Failed to upload file to node-1. Response: $UPLOAD_RESPONSE${NC}"
    exit 1
fi

echo "🔄 Synchronizing node-3 with node-1..."
./proxyma_cli sync --target https://localhost:8083 \
                   --peer-id node-1 \
                   --cert ./deploy/certs/node-3.crt \
                   --key ./deploy/certs/node-3.key \
                   --ca ./deploy/certs/ca.crt

echo "🔍 Verifying metadata replication on node-3 (Port 8083)..."
MAX_RETRIES=10
RETRY_COUNT=0
FILE_FOUND=false
MANIFEST_RESPONSE=""

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    MANIFEST_RESPONSE=$(curl -s --cacert ./deploy/certs/ca.crt \
        --cert ./deploy/certs/node-3.crt \
        --key ./deploy/certs/node-3.key \
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
SUBSCRIBE_RESPONSE=$(curl -s --cacert ./deploy/certs/ca.crt \
  --cert ./deploy/certs/node-3.crt \
  --key ./deploy/certs/node-3.key \
  -X POST "https://localhost:8083/subscribe?name=test_e2e.txt")
echo -e "${GREEN}✅ Subscription successful.${NC}"

echo "🔄 Forcing a second Sync to trigger the P2P download..."
./proxyma_cli sync --target https://localhost:8083 \
                   --peer-id node-1 \
                   --cert ./deploy/certs/node-3.crt \
                   --key ./deploy/certs/node-3.key \
                   --ca ./deploy/certs/ca.crt

echo "⬇️ Downloading the file from node-3's API..."
# Polling loop waiting for the async worker to finish the P2P download
for i in {1..5}; do
    curl -s -f --cacert ./deploy/certs/ca.crt \
      --cert ./deploy/certs/node-3.crt \
      --key ./deploy/certs/node-3.key \
      "https://localhost:8083/download/$FILE_HASH" -o downloaded_test.txt && break || sleep 1
done

echo "⚖️ Verifying cryptographic integrity of the content..."
if diff test_e2e.txt downloaded_test.txt > /dev/null; then
    echo -e "${GREEN}🎉 E2E Test Completed! The downloaded file from node-3 is bit-by-bit identical to the original.${NC}"
else
    echo -e "${RED}❌ Data corruption. The downloaded file does not match the original.${NC}"
    exit 1
fi

rm downloaded_test.txt
rm test_e2e.txt
