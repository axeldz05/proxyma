# Proxyma: Heterogeneous Resource Orchestrator (WIP)

**Proxyma** is a distributed P2P storage and compute orchestrator written in **Go**. Enrolled
servers, desktops, and Android devices can expose explicitly registered services, exchange VFS
metadata and content, and execute multi-node workflows over an authenticated mesh.

The project is under active development. The core mesh is implemented, but some integrations are still in development. The authoritative multi-node behavior currently covered by tests is documented in [`tests/e2e/README.md`](tests/e2e/README.md).

---

## Key Features

* **P2P VFS:** Replicates versioned metadata and tombstones, retrieves subscribed or requested
  blobs, verifies SHA-256 content, and preserves pending downloads across restarts.
* **Services and pipelines:** Runs registered unary and streaming services and coordinates
  multi-node DAGs. Registration validates graph structure and port compatibility when the relevant
  service schemas are available; runtime service inputs are also validated. Local file inputs and
  returned outputs can be staged through the VFS.
* **Connectivity:** Uses mTLS for protected inter-node routes, direct HTTP/QUIC where available,
  STUN-assisted UDP hole punching, IPv4 UPnP/NAT-PMP mapping, and long-polling relay fallback.
  Enrollment, probing, and relay bootstrap have intentional anonymous routes. An embedded TURN
  server (pion/turn, IPv4 and IPv6) plus TURN client fallback is available via `turn_enabled` /
  `turn_server` config; nodes that cannot discover a public endpoint keep QUIC over TURN relays,
  and the E2E suite does not validate a real consumer gateway's port mapping.
* **CLI, Go-mobile bind, and Android:** Provides a Cobra CLI, an in-process/Unix IPC binding layer,
  and a Jetpack Compose Android client. Android builds intentionally return unsupported for WebRTC
  service registration and signaling.
* **Streaming experiments:** HTTP/NDJSON streaming and non-Android WebRTC DataChannels are
  implemented. The current screen service emits generated JPEG frames; it is not real screen
  capture, remote input, or a latency-qualified remote-desktop protocol.
* **Example services:** `services-examples/` contains Python/`uv`, OCR, media, music, clipboard,
  shell, and collaboration labs. These demonstrate the service contract but are not all covered as
  production platform guarantees by the Docker E2E suite.

---

## Getting Started & CLI Usage

Proxyma can be run directly as a CLI binary across standalone nodes or distributed clusters.

> [!NOTE]
> **One Node Per Device vs. Multiple Nodes on the Same Machine**
> - **Standard (One Node Per Device)**: In typical deployments, each physical device, VM, or container runs a single Proxyma daemon. By default, every command connects via the local Unix domain socket using the canonical user directory (`~/.proxyma`, or `$PROXYMA_STORAGE`), so **no `--storage` flag is needed**.
> - **Local Simulation (Multiple Nodes on One Machine)**: When simulating a cluster locally on a single machine, multiple daemons cannot share the same `~/.proxyma` directory or the same TCP port (`8080`). Pass `--storage <dir>` and `--port <port>` to keep each node isolated (e.g. `--storage ./data-node1 --port 8080` vs `--storage ./data-node2 --port 8081`).

The walkthrough below demonstrates standard usage across two machines (**Device 1** and **Device 2**).

### 1. Build the Binary

```bash
go build -o proxyma ./cmd/proxyma
```

### 2. Cluster Lifecycle & Node Pairing

Proxyma uses mTLS with an internal Certificate Authority to secure all inter-node communication.

* **Initialize a new cluster root node (on Device 1):**
  Creates the cluster CA, issues certificates for `node-1`, and generates `~/.proxyma/config.json`:
  ```bash
  ./proxyma init --id node-1
  ```

* **Start the node daemon (on Device 1):**
  Binds TCP, QUIC, and Unix socket listeners:
  ```bash
  ./proxyma run
  ```

* **Generate a cluster invite token (on Device 1):**
  Mint a short-lived (15-minute) invite token:
  ```bash
  ./proxyma cluster invite
  ```

* **Join a second node to the cluster (on Device 2):**
  Enrolls the new node with Device 1's cluster CA using the token, generates node certificates, and writes `~/.proxyma/config.json`:
  ```bash
  ./proxyma cluster join --token "<invite-token>" --node_id node-2
  ```

* **Start the second node daemon (on Device 2):**
  ```bash
  ./proxyma run
  ```

* **View connected peers (on either device):**
  ```bash
  ./proxyma peers list
  ```

### 3. Virtual File System (VFS)

The P2P VFS synchronizes metadata across all nodes and transfers binary content on-demand with SHA-256 validation.

* **Upload a local file into the mesh (on Device 1):**
  ```bash
  ./proxyma storage upload --name dataset.csv --path ./local-dataset.csv
  ```

* **List files across the cluster snapshot (on Device 2):**
  ```bash
  ./proxyma storage list
  ```

* **Download on-demand and open a file (on Device 2):**
  ```bash
  ./proxyma storage open --name dataset.csv
  ```

* **Subscribe to automated updates (on Device 2):**
  Subscribed files are automatically staged and synchronized when updated by any peer:
  ```bash
  ./proxyma storage subscribe --name dataset.csv
  ./proxyma storage unsubscribe --name dataset.csv
  ```

* **Trigger explicit synchronization:**
  ```bash
  ./proxyma storage sync
  ```

* **Purge cached physical content or tombstone a file:**
  ```bash
  ./proxyma storage purge --name dataset.csv  # clears local physical cache on this node
  ./proxyma storage delete --name dataset.csv # publishes tombstone to cluster
  ```

### 4. Compute Services & Pipelines

Nodes can expose unary, streaming, and file transformation services, which can be composed into distributed DAG pipelines.

* **Register a service on the local node (on Device 1):**
  You can register a service via command-line flags or a schema JSON file:
  ```bash
  # Via CLI flags:
  ./proxyma service add --name echo --desc "Echo JSON" --exec 'sh -c '\''echo "{\"status\":\"ok\",\"reply\":\"$msg\"}"'\''' --param "msg:string" --type exec

  # Or from a schema file:
  ./proxyma service add --schema-file ./services-examples/media/resize/resize_service.json
  ```

* **Discover active services in the cluster (on Device 2):**
  ```bash
  ./proxyma service discover
  ```

* **Execute a service (from Device 2):**
  Dispatch execution directly or leverage scheduling strategies (`fastest`, `cheapest`, `low_power`):
  ```bash
  ./proxyma service run --name echo --inputs "msg=hello-mesh"
  ./proxyma service run --name echo --inputs "msg=hello-mesh" --strategy fastest
  ```

* **Register and run a multi-step pipeline DAG:**
  ```bash
  # Register pipeline schema across the mesh (Device 1)
  ./proxyma service add_pipeline --id media-thumb --schema-file ./services-examples/media/thumbnail_pipeline.json

  # List registered pipelines (Device 2)
  ./proxyma service list_pipelines

  # Execute pipeline across the mesh (Device 2)
  ./proxyma service run_pipeline --id media-thumb --payload '{"image_path": "photo.jpg"}'
  ```

* **Query task execution status:**
  ```bash
  ./proxyma service status --task_id <task-id>
  ```

### 5. Telemetry & Observability

* **Live bandwidth monitor:**
  ```bash
  ./proxyma telemetry stats
  ```

* **Node logs:**
  ```bash
  ./proxyma telemetry logs
  ```

> [!TIP]
> **Simulating Multiple Nodes on One Machine**
> If you want to experiment with a cluster locally on a single machine, specify distinct `--storage` and `--port` parameters for each instance:
> ```bash
> # Terminal 1 (Node 1)
> ./proxyma init --id node-1 --port 8080 --storage ./data-node1
> ./proxyma run --storage ./data-node1
>
> # Terminal 2 (Node 2)
> TOKEN=$(./proxyma cluster invite --storage ./data-node1)
> ./proxyma cluster join --token "$TOKEN" --node_id node-2 --port 8081 --storage ./data-node2
> ./proxyma run --storage ./data-node2
> ```

---

## Pipeline Editors

Proxyma includes two interfaces for creating and editing distributed pipeline schemas:

1. **Terminal editor:** An interactive menu-driven TUI launched with
   `proxyma service edit_pipeline`.
2. **Android editor:** A Jetpack Compose editor in the *Services* area with service and target-node
   selection. Dynamic service forms use schema metadata for options and native file/camera pickers.

The Go validation engine remains authoritative when a pipeline is saved. Android is covered by
JVM/Robolectric delegated-Web-bridge and picker-intent contracts, lint, assembly, and Go-mobile
ABI checks; the repository does not currently run instrumented device UI/JNI tests.

---

## Developer Quickstart

To quickly spin up a developer test environment:

1. Run the developer bootstrapping script:
   ```bash
   ./scripts/bootstrap_dev.sh
   ```
   This builds the CLI and example editors, starts a local daemon, registers every
   `*_service.json` and `*_pipeline.json` under `services-examples/`, and pre-populates sample VFS
   files. The default state directory is `$HOME/.proxyma_dev`; override it with
   `PROXYMA_DEV_DIR=/path/to/state`.

   Python examples require their external tools. If `uv` is installed, the script attempts to sync
   the example environment; unavailable optional examples do not prevent the rest of the
   bootstrap from starting.

2. Interact with the node easily using the automatic wrapper (which binds `--storage` flags by default):
   * List files: `$HOME/.proxyma_dev/pm storage list`
   * Discover services: `$HOME/.proxyma_dev/pm service discover`
   * Launch the TUI editor: `$HOME/.proxyma_dev/pm service edit_pipeline`

To build and compile the mobile app bindings:
   ```bash
   ./cmd/proxyma-android/ship_to_attached_phone.sh
   ```

---

## Verification

The main local gates are:

```bash
make test-cover          # Go tests plus per-package coverage ratchet
make test-integration    # live bind/server contracts plus isolated developer-bootstrap smoke
make test-race           # complete Go race-detector pass
make test-android        # fresh AAR, ABI, JVM/Robolectric contracts, lint, assemble
make test-e2e-pr         # deterministic public contracts
make test-e2e-network    # topology and fault-injection contracts
make test-e2e-full       # all stable Docker E2E contracts
```

The checked-in unit coverage floors range from 53.8% to 93.1% by package. Aggregate unit, E2E,
and union percentages are informational; the per-package unit ratchet is the enforced coverage
contract. The stable E2E profile covers the public behaviors listed in
[`tests/e2e/README.md`](tests/e2e/README.md), not every roadmap objective below.

---

## Roadmap & Status

### Phase 1: Core & Networking
- [x] Decentralized Virtual File System (VFS) with P2P synchronization.
- [x] Persistent cluster peer topology database.
- [x] CSR enrollment and mTLS authorization for protected inter-node routes.
- [x] UDP NAT Hole Punching and direct QUIC connectivity.
- [x] IPv4 UPnP/NAT-PMP implementation and STUN discovery.
- [x] TURN fallback (embedded pion/turn server + relayed QUIC dialing).
- [ ] Hardware-gateway E2E validation of TURN/relay fallback on real consumer gateways.

### Phase 2: Orchestration & Services
- [x] Custom services engine and workflow orchestration.
- [x] Registration-time pipeline cycle, topology, and known-schema port validation.
- [x] Runtime service input validation and cross-node VFS staging/fetching.
- [x] Resource-aware bid metadata and `fastest`, `cheapest`, and `low_power` strategies.
- [ ] A deterministic green E2E contract for load-aware selection; OpenTelemetry export currently
      provides best-effort observability rather than driving scheduling.
- [ ] A precise public contract for any additional "dynamic port validation" behavior.

### Phase 3: Ecosystem & UI
- [x] Standalone menu-driven pipeline editor (TUI).
- [x] Jetpack Compose-based visual pipeline editor on Android with target node selection.
- [x] Lightweight Android daemon client (built with Gomobile JNI) with native camera & file pickers.
- [ ] Public HTTP/CLI multi-message bidirectional streaming and end-to-end cancellation.
- [ ] Real low-latency screen capture, rendering, remote input, and latency/jitter targets.
- [ ] WebRTC service and signaling support on Android.

---

## License

Proxyma is open source software licensed under the [Apache License, Version 2.0](LICENSE).

