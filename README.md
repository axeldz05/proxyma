# Proxyma: Heterogeneous Resource Orchestrator (WIP)

**Proxyma** is a distributed P2P storage and compute orchestrator written in **Go**. Enrolled
servers, desktops, and Android devices can expose explicitly registered services, exchange VFS
metadata and content, and execute multi-node workflows over an authenticated mesh.

The project is under active development. The core mesh is implemented, but some integrations are still lab-grade. The authoritative multi-node behavior currently covered by tests is documented in [`tests/e2e/README.md`](tests/e2e/README.md).

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
  Enrollment, probing, and relay bootstrap have intentional anonymous routes. TURN is not
  implemented, and the E2E suite does not validate a real consumer gateway's port mapping.
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

## Pipeline Editors

Proxyma includes two interfaces for creating and editing distributed pipeline schemas:

1. **Terminal editor:** An interactive menu-driven TUI launched with
   `proxyma service edit_pipeline`.
2. **Android editor:** A Jetpack Compose editor in the *Services* area with service and target-node
   selection. Dynamic service forms use schema metadata for options and native file/camera pickers.

The Go validation engine remains authoritative when a pipeline is saved. Android is covered by
JVM contract tests, lint, assembly, and Go-mobile ABI checks; the repository does not currently
run instrumented device UI tests.

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
make test-race           # complete Go race-detector pass
make test-android        # Android build-tag contracts, fresh AAR, JVM tests, lint, assemble
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
- [ ] Hardware-gateway E2E validation and TURN fallback.

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
