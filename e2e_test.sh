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

# We need to skip TLS verification (-k) because our host doesn't have the cluster CA installed.
echo "Hello Proxyma Cluster!" > test_e2e.txt

UPLOAD_RESPONSE=$(curl -s \
  --cacert ./deploy/certs/ca.crt \
  --cert ./deploy/certs/node-1.crt \
  --key ./deploy/certs/node-1.key \
  -X POST \
  -F "file=@test_e2e.txt" \
  https://localhost:8081/upload)

rm test_e2e.txt

if echo "$UPLOAD_RESPONSE" | grep -q "successfully"; then
    echo -e "${GREEN}✅ File successfully uploaded to node-1.${NC}"
else
    echo -e "${RED}❌ Failed to upload file to node-1. Response: $UPLOAD_RESPONSE${NC}"
    exit 1
fi

echo "🔄 Synchronizing node-3 with node-1..."
./proxyma_cli sync --target https://localhost:8083 \
                   --peer-id node-1 \
                   --peer-addr https://node-1:8081 \
                   --cert ./deploy/certs/node-3.crt \
                   --key ./deploy/certs/node-3.key \
                   --ca ./deploy/certs/ca.crt

echo "🔍 Verifying replication on node-3 (Port 8083)..."
MAX_RETRIES=10
RETRY_COUNT=0
FILE_FOUND=false

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    MANIFEST_RESPONSE=$(curl -s \
        --cacert ./deploy/certs/ca.crt \
        --cert ./deploy/certs/node-3.crt \
        --key ./deploy/certs/node-3.key \
        https://localhost:8083/manifest || echo "failed")
    if echo "$MANIFEST_RESPONSE" | grep -q "test_e2e.txt"; then
        FILE_FOUND=true
        break
    fi

    echo "   ... File not found yet. Retrying ($RETRY_COUNT/$MAX_RETRIES)..."
    sleep 2
    RETRY_COUNT=$((RETRY_COUNT+1))
done

if [ "$FILE_FOUND" = true ]; then
    echo -e "${GREEN}🎉 E2E test completed successfully! The file was correctly replicated.${NC}"
else
    echo -e "${RED}❌ E2E test failed. The file never reached node-3.${NC}"
    echo "📜 Last response from node-3: $MANIFEST_RESPONSE"
    exit 1
fi
