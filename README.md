# Proxyma: Heterogeneous Resource Orchestrator (WIP)

**Proxyma** is a distributed, heterogeneous P2P resource and compute orchestrator written in **Go**. It unifies devices (servers, desktops, mobiles) into a single intelligent computing and storage mesh. It allows the hardware capabilities of one node (such as a PC's compute services or a mobile camera) to be transparently shared across the network.

---

## Key Features

* **P2P Synchronization & VFS:** A decentralized virtual file system with background metadata replication and blob retrieval (inspired by BitTorrent).
* **Compute Workflows & Pipeline Engine:** Coordinates tasks across nodes using dynamic pipeline orchestration (workflows/DAGs) with static/dynamic port type checks and transparent cross-node VFS auto-staging & auto-fetching.
* **Heterogeneous Connectivity:** Native integration between servers, desktop PCs, and mobile devices (using Go-mobile JNI bindings).
* **NAT Traversal & Hole Punching:** Implements direct UDP NAT Hole Punching using STUN for public IP/port discovery, signaling via the Sponsor relay, automatic UPnP/NAT-PMP port mapping, and secure QUIC tunnels. Falls back to long-polling relay fallback in strict firewalls.
* **E2E mTLS Security:** Dynamic CSR enrollment, mutual TLS authorization for all inter-node calls, and automatic cluster topology sync.
* **Native Python Services & UV Environment:** Native Python service execution (`ocrmypdf`, `pypdf`, `pytesseract`) with isolated `uv` virtual environment resolution and RGBA alpha channel image pre-conversion.

---

## Standalone Pipeline Editor

Proxyma includes a visual, standalone blueprint/node-editor to create, edit, and validate distributed workflow pipelines:

1. **Static Validation Engine**: Performs DAG cycle detection and strict type verification on linked ports (e.g. matching an OCR output parameter to a save-file input parameter) before pipeline registration.
2. **Platform Interfaces**:
   * **Cobra CLI (TUI)**: An interactive terminal UI editor launched with `proxyma service edit_pipeline`.
   * **Android Client (Jetpack Compose GUI)**: Built modularly in Jetpack Compose, integrated directly inside the *Services* tab of the Android app, supporting node target selection, native camera/file pickers, preset dropdowns (`UISchema`), and execution via Go-mobile bindings.

---

## Developer Quickstart

To quickly spin up a developer test environment:

1. Run the developer bootstrapping script:
   ```bash
   ./scripts/bootstrap_dev.sh
   ```
   This script builds all binaries, launches a local background daemon (scoped to `/tmp/proxyma-dev`), registers mock services (`ocr`, `text/extract`, `obsidian/save`, and the standalone `pipeline/editor`), and pre-populates VFS sample files.

2. Interact with the node easily using the automatic wrapper (which binds `--storage` flags by default):
   * List files: `/tmp/proxyma-dev/pm storage list`
   * Discover services: `/tmp/proxyma-dev/pm service discover`
   * Launch TUI Editor: `/tmp/proxyma-dev/pm service edit_pipeline`

To build and compile the mobile app bindings:
   ```bash
   ./cmd/proxyma-android/ship_to_attached_phone.sh
   ```

---

## Roadmap & Status

### Phase 1: Core & Networking
- [x] Decentralized Virtual File System (VFS) with P2P synchronization.
- [x] Persistent cluster peer topology database.
- [x] End-to-end mTLS authentication and secure "Handshake" pairing.
- [x] UDP NAT Hole Punching and direct QUIC connectivity.
- [x] Automatic UPnP/NAT-PMP port mapping and STUN gateway queries.

### Phase 2: Orchestration & Services
- [x] Custom services engine and workflow orchestration.
- [x] **Static & Dynamic pipeline validation** (cycle/type check).
- [x] **Transparent cross-node VFS file auto-staging and auto-fetching**.
- [ ] Load balancing logic based on real-time CPU/RAM telemetry (OpenTelemetry).

### Phase 3: Ecosystem & UI
- [x] Standalone visual blueprint pipeline editor (TUI).
- [x] Jetpack Compose-based visual pipeline editor on Android with target node selection.
- [x] Lightweight Android daemon client (built with Gomobile JNI) with native camera & file pickers.
- [ ] Low-latency streaming protocol for remote screen access.
