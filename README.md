# Proxyma: Heterogeneous Resource Orchestrator (WIP)

**Proxyma** is a distributed system written in **Go**, designed to unify multiple devices into a single, intelligent computing and storage mesh. 
It acts as a connective tissue that allows the hardware capabilities of one node (such as a PC's GPU or a mobile camera) to be transparently available to the rest of the network of nodes.  
The goal is to eliminate hardware boundaries, integrating multiple custom services.

### **Issues in mind**
* NAT Traversal: Implementing hole punching and relay fallbacks (STUN/TURN) to enable connectivity between nodes behind restrictive firewalls. Consider using AutoNAT with libp2p
* Currently, the system uses BoltDB for subscriptions of files and Virtual File System (VFS). Considering the transition to BadgerDB (which implements a WiscKey LSM-based) or a customized 
implementation to optimize I/O on SSDs and handle larger metadata sets efficiently.
* Distributed Consistency: Moving beyond local ACID compliance towards a distributed consensus model (e.g., Raft) for unified network state.
* The "custom services" engine currently relies on a custom JSON-based protocol, which introduces serialization overhead. Consider using gRPC for Inter-Node Communication and Streaming, in conjunction
with grpc-gateway for particular cases where a JSON will work just fine.

## Key Features
* **P2P Synchronization:** A file transfer protocol inspired by BitTorrent.
* **The "custom services":** The system has a suite of services provided by its devices.
* **Heterogeneous Connectivity:** Native integration between servers, desktop PCs, and mobile devices.
* **Remote Screen Management:** Ability to visualize and control interfaces between nodes in the network.

## To-Do

### Phase 1: Core & Networking
- [x] Implement a File System for servers and clients, based on a P2P Syncrhonization.
- [ ] Implement automatic node discovery in local networks (mDNS).
- [ ] Implement global discovery using a secure pairing.
- [x] End-to-end TLS encryption.
- [ ] Secure "Handshake" and pairing system for new devices.

### Phase 2: Orchestration & Services
- [ ] **Custom services** engine.
- [ ] Load balancing logic based on real-time CPU/RAM telemetry (OpenTelemetry) from nodes.

### Phase 3: Ecosystem & UI
- [ ] Lightweight Android client (Photo capture, video recording, task submission).
- [ ] Low-latency streaming protocol for remote screen access.
- [ ] CLI Dashboard to monitor network health and load.

---
