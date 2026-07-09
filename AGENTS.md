# Proxyma - Agent Context & Reference Guide

This file serves as a condensed context window for AI agents. It summarizes the architecture, file layout, key modules, and the direction of **Proxyma**, with direct file and symbol links to ensure rapid onboarding for any new task.

---

## 🎯 Project Overview & Goal
**Proxyma** is a distributed, heterogeneous P2P resource and compute orchestrator written in Go. Its goal is to unify devices (servers, desktops, mobiles) into a single mesh where hardware capabilities (compute tasks, storage, remote screen/camera) are transparently shared.

---

## 📁 Repository Structure & Key Components

### 1. Command Line Interface (CLI) Layer (`cmd/`)
The CLI is built using Cobra, located under [cmd/proxyma](file:///home/drusila/Projects/proxyma/cmd/proxyma).
* **Entry point**: [main.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/main.go)
* **Root config**: [root.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/root.go)
* **Daemon runner**: [run.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/run.go)
* **Shared helpers**: [helpers.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/helpers.go) (handles configurations, logging init via [NewLogger](file:///home/drusila/Projects/proxyma/internal/protocol/protocol.go#L13))
* **Pairing**: [invite.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/invite.go) and [join.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/join.go)
* **VFS & Sync**: [vfs.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/vfs.go) and [sync.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/sync.go) (commands that interact with the daemon over local Unix socket)
* **Compute Services**: [service.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/service.go)
* **Android / Mobile Bindings**:
  * [proxyma-bind/bind.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/bind.go) (gomobile export bindings)
  * [proxyma-android](file:///home/drusila/Projects/proxyma/cmd/proxyma-android) (Android app structure)

### 2. Core Server & Orchestration Layer (`internal/server/`)
Coordinates local resources, routes inter-node HTTP/mTLS requests, and implements the relay fallback.
* **Orchestrator**: [Server](file:///home/drusila/Projects/proxyma/internal/server/server.go#L26-L48) (coordinates compute, storage, and background tasks)
* **Handlers**: [handlers.go](file:///home/drusila/Projects/proxyma/internal/server/handlers.go) (contains HTTP routing rules and the client identity validator [mTLSGuard](file:///home/drusila/Projects/proxyma/internal/server/handlers.go#L19))
* **Relay Fallback**: [relay.go](file:///home/drusila/Projects/proxyma/internal/server/relay.go) (facilitates connectivity behind restrictive firewalls via long polling)
* **Bandwidth Telemetry**: [bandwidth.go](file:///home/drusila/Projects/proxyma/internal/server/bandwidth.go) (tracks real-time network usage)

### 3. P2P Client & Secure Networking Layer (`internal/p2p/`)
Manages P2P connections, cryptographic handshakes, and certificates.
* **Client interface & client RPC**: [p2p_client.go](file:///home/drusila/Projects/proxyma/internal/p2p/p2p_client.go) (specifically [DownloadBlob](file:///home/drusila/Projects/proxyma/internal/p2p/p2p_client.go#L80) for fetching replicas)
* **Security & TLS**: [tls.go](file:///home/drusila/Projects/proxyma/internal/p2p/tls.go) (handles CSR parsing/signing, CA rotation, and cert generation)
* **Topology Join**: [join.go](file:///home/drusila/Projects/proxyma/internal/p2p/join.go) (implements remote node enrollment)

### 4. Storage Engine & Virtual File System (`internal/storage/`)
Manages VFS metadata index and replicates blobs asynchronously.
* **Engine**: [StorageEngine](file:///home/drusila/Projects/proxyma/internal/storage/storage_engine.go#L17) (coordinates file indexing, remote metadata tracking, and downloads)
* **VFS database**: [vfs.go](file:///home/drusila/Projects/proxyma/internal/storage/vfs.go) (tracks metadata on BoltDB)
* **Disk reader/writer**: [storage.go](file:///home/drusila/Projects/proxyma/internal/storage/physical/storage.go) (reads/writes blobs physical files on local filesystem)

### 5. Compute Engine (`internal/compute/`)
Executes custom jobs asynchronously with concurrency control.
* **Engine**: [ComputeEngine](file:///home/drusila/Projects/proxyma/internal/compute/compute.go#L15) (executes tasks inside a semaphore-controlled pool)
* **Execution Builders**: [handlerBuilder.go](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go) (converts local scripts/execs via [BuildScriptHandler](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go#L15) or gRPC microservices via [BuildGRPCHandler](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go#L43) into task executors)

### 6. Common Protocol Defs (`internal/protocol/`)
* **Schemas**: [protocol.go](file:///home/drusila/Projects/proxyma/internal/protocol/protocol.go) (contains definitions of `TaskRequest`, `IndexEntry`, `NodeConfig`, `UnixRequest`, etc.)

---

## 🔑 Key Workflows

### A. Pairing / Joining the Cluster
1. **Invite Generation**: A sponsor node generates a token via `proxyma invite` (handled by [InviteManager](file:///home/drusila/Projects/proxyma/internal/server/invite.go)).
2. **CSR Submission**: The client generates a private key/CSR and sends it to the sponsor (`/cluster/join`).
3. **mTLS Setup**: The sponsor signs the CSR via [tls.go](file:///home/drusila/Projects/proxyma/internal/p2p/tls.go#L95) and replies with the node certificate, CA certificate, and current topology.
4. **mTLS Guard**: Inter-node traffic requires certificates and is authorized by [mTLSGuard](file:///home/drusila/Projects/proxyma/internal/server/handlers.go#L19).

### B. File Sync & VFS Replication
1. **Local Control**: `proxyma sync` commands the local daemon using a Unix domain socket (`proxyma.sock`) to execute a sync sequence, avoiding HTTP exposure (see [sync.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/sync.go) and `handleUnixConnection` in [server.go](file:///home/drusila/Projects/proxyma/internal/server/server.go#L184)).
2. **Manifest Processing**: The node fetches remote manifests, processes them via [StorageEngine](file:///home/drusila/Projects/proxyma/internal/storage/storage_engine.go#L17), and pushes missing entries to `downloadQueue`.
3. **Async Retrieval**: Dedicated workers download and write missing blobs in the background using `downloadWorker` in [server.go](file:///home/drusila/Projects/proxyma/internal/server/server.go#L633).

### C. Compute Execution & Bidding
1. **Task Request**: An outgoing task is scheduled (`DispatchTask`).
2. **Service Bid**: The client asks the cluster for bids matching the resource requirement. The best bid is selected based on a cost/performance strategy (Fastest, Cheapest, Low Power) defined in [protocol.go](file:///home/drusila/Projects/proxyma/internal/protocol/protocol.go#L105).
3. **Execution**: The target node runs the task via `script`/`exec` handler or a custom `gRPC` client stub using [handlerBuilder.go](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go).

---

## 🔮 Future Direction & Issues in Progress

Based on open issues ([issues](file:///home/drusila/Projects/proxyma/issues) and [README.md](file:///home/drusila/Projects/proxyma/README.md)):
1. **NAT Traversal**: Implement full direct hole-punching using STUN/TURN or libp2p AutoNAT to reduce reliance on the Sponsor relay fallback.
2. **Database Migration**: Shift the VFS metadata from BoltDB to BadgerDB to improve SSD write performance and handle large directories efficiently.
3. **Distributed Consensus**: Incorporate Raft or a similar consensus protocol to maintain uniform mesh state without a single coordinating authority.
4. **gRPC/Streaming**: Shift the JSON-based protocol to gRPC/Protobuf streams for high-throughput, low-latency node-to-node connection. Integrate WebRTC for raw data/streaming tasks.
5. **Microkernel Refactoring**: Shift to a modular design centering around a service registry/microkernel.
