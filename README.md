# Proxyma: Heterogeneous Resource Orchestrator (WIP)

**Proxyma** is a distributed system written in **Go**, designed to unify multiple devices into a single, intelligent computing and storage mesh. 
It acts as a connective tissue that allows the hardware capabilities of one node (such as a PC's GPU or a mobile camera) to be transparently available to the rest of the network.  
The goal is to eliminate hardware boundaries, integrating multiple custom services.

### **Issues in mind**
For connecting outside a local network, the system will need to have an implementation of a global discovery. This needs to be able to authenticate the devices in some way.  
There's also the consideration of two devices not being able to connect to each other because they're behind a NAT to make the connection, 
and it may not be able to open a port which would be directly accesible through the internet. This may need a relay between the devices.

## Key Features
* **P2P Synchronization:** A file transfer protocol inspired by BitTorrent.
* **The "custom services":** The system has a suite of services provided by its devices.
* **Heterogeneous Connectivity:** Native integration between servers, desktop PCs, and mobile devices.
* **Remote Screen Management:** Ability to visualize and control interfaces between nodes in the network.

## To-Do

### Phase 1: Core & Networking
- [ ] Implement automatic node discovery in local networks (mDNS).
- [ ] End-to-end TLS encryption.
- [ ] Secure "Handshake" and pairing system for new devices.

### Phase 2: Orchestration & Services
- [ ] **Custom services** engine.
- [ ] Load balancing logic based on real-time CPU/RAM telemetry from nodes.

### Phase 3: Ecosystem & UI
- [ ] Lightweight Android client (Photo capture, video recording, task submission).
- [ ] Low-latency streaming protocol for remote screen access.
- [ ] CLI Dashboard to monitor network health and load.

---
