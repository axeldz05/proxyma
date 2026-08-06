# Proxyma - Agent Context & Reference Guide

Condensed context for AI agents: architecture, SSOT helpers, what not to reinvent, and coding rules.

**Keep in sync with [`.cursorrules.md`](../.cursorrules.md).** Agents must update both files and relevant skills when finishing implementations that change structure or conventions (see Mandatory section below).

---

## Project Goal

**Proxyma** is a distributed, heterogeneous P2P resource and compute orchestrator (Go). It unifies devices (servers, desktops, mobiles) into one mesh that shares compute, storage, and related capabilities over mTLS (with QUIC hole-punch and HTTP relay fallback).

---

## Mandatory: Keep Skills Up to Date

**At the end of every implementation** (feature, refactor, bug fix that changes structure, APIs, or conventions), agents **MUST**:

1. Update [`.cursorrules.md`](../.cursorrules.md) **and** this file if file layout, SSOT helpers, or workflows changed.
2. Update the relevant skill(s) under [`.agents/skills/`](skills/) so future agents see current entry points, “do not reinvent”, and boundaries.
3. Prefer editing existing skills over inventing parallel docs.

Skip only for purely cosmetic changes with no behavioral or structural impact.

---

## Design Rules (Always On)

* **Golden rule (duplication)**: If changing one behavior requires touching **>2 code zones**, compress into a shared helper. Do **not** copy service CRUD, peer fan-out, relay forward, blob fetch/stage, or unix dial stacks.
* **Continuous granularity (3 tiers)**: Unix: `DialUnix` → Write/Read/Scan → `sendUnixSocketCommand` / `dispatchUnixOrLocal` / `dispatchUnixLocalOrOffline` / `dispatchUnixStreamOrLocal`.
* **UI hints**: `protocol.InferUIHint` / `EffectiveUIHint`; Android uses `FormParameter.isFilePicker()` — no name sniffing.
* **Bind errors**: `BindErrorJSON` / `ParseBindError` / `IsBindError` only (no double-wrap, no `"error:"` prefix).
* **In-memory registry**: Persist `services.json` only on mutation; runtime via `Compute.GetService` / `GetHandler`.
* **No unrequested commits**.

---

## Repository Map (Current)

### CLI — `cmd/proxyma/`
* `root.go` — Cobra registration + `Execute`.
* `cli_actions.go` — `executeActionLocal` + resolve helpers (`IsBindError`).
* `cli_render.go` / `cli_open.go` — formatting / editor+open.

### Bindings — `cmd/proxyma-bind/`
* L1 IPC + `dispatchUnixOrLocal` / `dispatchUnixLocalOrOffline` / `dispatchUnixStreamOrLocal`.
* `LocalServiceDetail` via bind schema paths; `BindErrorJSON` / `IsBindError` (StartNode/ChangeStorage too).
* `GetServiceSchema` offline arm; `resolveServiceSchema` / `GetServiceDetails`; `RunPipeline` → `RunService`.

### Server — `internal/server/`
* `server.go` lifecycle; `peers.go` topology; `advertisedTCPPort` / `configTCPPort` (`protocol.DefaultTCPPort`).
* `catalog_kinds.go` — `catalogKinds` / `syncCatalogToPeer` / `lookupCachedServiceSchema` / `resolveServiceBidTarget`.
* `service_catalog.go` — Detail/Discover/Add/Remove, **`applyServiceAction`**, `NotifyService*`.
* `service_exec.go` — Run/Stream + ingest (`ResultLocalPath`); **`submitTrackedTask`**.
* `applyPipelineAction` / `NotifySchema*`; `ValidatePipelineSchema` (incl. cycle); `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer` + **`gossipToPeer` / `gossipAll`** + `PeerRPC*`.
* `peerCNFromRequest` / `requirePeerCNMatchesBodyID`; HTTP mounts use **`protocol.Path*`** (`handlePeerIDAction`, schema notify re-validates).
* `EstimateTaskCost` / `selectBestServiceBid`; relay **`OriginPeerID`** + response body cap.

### P2P — `internal/p2p/`
* `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr`, `CAKeyPath`, `CACertPaths`, `NodeCertPaths`, `ReadCAPEM` / `ResolveNodeCertPaths`, `PeerCNFromTLS` / `VerifyTLSPeerCN`, `newNodeCertTemplate`, `signLeaf`, `LeafDNSNames`, `CSRCommonName`, `NewHTTPClient`, `PostJSONAbsolute`, `ForwardRelay`, `NewRelayRequest`, `FlattenHTTPHeader`, `LoadNodeTLS`.
* `HashCertDER` / `CAHashFromPEM` / `TLSConfigTrustCAHash` / `WriteNodePEMs`.
* `NATMapper.SetOnMapped`; `HolePunchPingPayload` / `ParseHolePunchPing` / `BurstPings`; `routeOverQUICSession`.

### Storage / Compute / Protocol
* `UpsertAndSubscribe` / `deleteBlobIfOrphan`; bolt JSON + `boltPutFlag` / `boltHasKey`.
* `utils.WriteNDJSON` / `PumpJSON*` / `ForEachNDJSON` / `ScanNDJSON`; `ReadJSONFile` / `WriteJSONFile`.
* `compute.EstimateTaskCost`; `protocol.Path*` / `PathRel` / `MaxRelayBodyBytes`, `RPCTimeout*`, `DefaultTCPPort`, `NormalizeServiceSchema`, `DescribeParameter`, `MissingRequired`, `ActionAdd`/`Remove`, `ResultLocalPath`, `VFSURI` / `IsStageableLocalPath` / `RewriteLocalFilePaths` / `InferUIHint` / `IsFilePickerHint`, `RelayRequest.OriginPeerID`.

### Bindings / Android
* `LookupServiceSchema`→`resolveServiceSchema`, `ResolveLocalBlob`, `ResolveTaskResultPath`; CLI uses PersistentFlag `cliStorage` only.
* Android: `fetchServiceDetail` / `loadServiceDetail` / `loadServiceDetailsMap` / `loadRunSpecs` / `formParameterFrom` / `taskStatusColor`; `getResultPath` → bind `ResolveTaskResultPath`; trust bind `uiHint`.

---

## What Already Exists (Do Not Reinvent)

| Need | Use |
|------|-----|
| services.json | `compute.Load/Save/Build/Upsert/Delete*` |
| Schema resolve | `LocalServiceDetail` / `LookupServiceSchema` / `GetServiceSchema` |
| Schema normalize | `NormalizeServiceSchema` / `DescribeParameter` / `ActionAdd` |
| UI hint | `InferUIHint` / `EffectiveUIHint` / `IsFilePickerHint` / `IsImagePickerHint` |
| Missing required | `MissingRequired` |
| Result path | `ResultLocalPath` / `OutputHashFromOutputs` / bind `ResolveTaskResultPath` |
| Peer fan-out | `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer` |
| Gossip | `gossipToPeer` / `gossipAll` / `syncCatalogToPeer` / `catalogKinds` |
| Notify outbox | `enqueueOutbox` / `flushOutbox` / `OutboxPendingCount` (Bolt `notify_outbox`) |
| Service subscribe | `LocalServiceSubscribe` / `SetServiceSubscription` / `IsServiceSubscribed` / `MatchServicePattern` |
| Bid strategy | `NormalizeSortStrategy` / `LocalServiceRun(..., strategy)` / `--strategy` |
| OTel bid export | `telemetry.ExportBidAsync` / `InitFromEnv` / `SetBidExporter` |
| Pipeline/service apply | `applyPipelineAction` / `applyServiceAction` / `submitTrackedTask` |
| Service gossip | `NotifyService` / `NotifyServiceToPeer` |
| Blob fetch / CAS | `fetchBlobFromPeer` / `SaveVerifiedPhysicalBlob` / `UpsertAndSubscribe` / `deleteBlobIfOrphan` / `IsValidCASHash` / `StageAndRewrite` / `protocol.RewriteLocalFilePaths` |
| VFS local ops | `LocalVFSUpload` / `ResolveLocalBlob` / `StageLocalFile` / `StageAndRewrite` |
| VFS URI | `protocol.VFSURI` / `ParseVFSURI` / `IsStageableLocalPath` |
| HTTP paths / relay | `protocol.Path*` / `NewRelayRequest` / `RequestPathWithQuery` / `MaxRelayBodyBytes` |
| RPC timeouts | `protocol.RPCTimeout*` / `PeerRPC*` |
| Default TCP port | `protocol.DefaultTCPPort` |
| Unix IPC | Dial/Write/Read/Scan + dispatch* / `dispatchUnixStreamOrLocal` |
| Bind errors | `BindErrorJSON` / `ParseBindError` / `IsBindError` |
| Cert / CA hash / PEM write | `WriteNodePEMs` / `signLeaf` / `LeafDNSNames` / `CAHashFromPEM` / `ReadCAPEM` / `ResolveNodeCertPaths` / `TLSConfigTrustCAHash` / `CSRCommonName` |
| QUIC addr | `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr` |
| NAT map callback | `NATMapper.SetOnMapped` / `refreshPublicUDPFromMapping` |
| NDJSON | `utils.WriteNDJSON` / `PumpJSON*` / `ForEachNDJSON` / `ScanNDJSON` |
| HTTP client | `NewHTTPClient` / `PostJSONAbsolute` |
| Hole-punch | `BurstPings` / `HolePunchPingPayload` / `ParseHolePunchPing` / `RespondToHolePunch` / `QUICManager.ReloadTLS` |
| JSON files | `ReadJSONFile` / `WriteJSONFile` |
| Service bid cost | `Compute.EstimateTaskCost` / `BuildServiceBid` / `selectBestServiceBid` (`CostUnits` / `PowerScore`) |
| HTTP stream handlers | `BuildHTTPBidiHandler` / `BuildHTTPServerStreamHandler` via `BuildHandler` |
| WebRTC DataChannel | `BuildWebRTCHandler` / `BuildWebRTCHandlerWithClient` / `AcceptWebRTCOfferEcho` (`Type=webrtc`) |
| WebRTC signaling | `POST` `protocol.PathWebRTCSignal` → `HandleWebRTCSignal` (mTLS echo answerer) |
| Screen frame stream | `BuildScreenHandler` / `ServiceTypeScreen` (`fake` JPEG NDJSON `{n,frame_b64}`) |

---

## Key Workflows

### Pairing
1. `LocalInviteGenerate` → `(token, expires)` + `DefaultInviteMinutes`.
2. Join CSR → `/cluster/join` (or `ForwardRelay`).
3. `mTLSGuard` on inter-node HTTP (`peerCNFromRequest`).

### VFS / Compute
1. `announceAndSync` + `forEachPeer` manifests → `fetchBlobFromPeer`.
2. Bids via `mapEachPeer`; on-demand via `firstPeer`; dispatch with `StageLocalFile`.

---

## Skills Index

See [`.cursorrules.md`](../.cursorrules.md) skills table (`architecture-and-refactor-auditor`, `semantic-compression`, `continuous-granularity`, P2P/Android/testing/uischema/tdd).

---

## Roadmap (Context Only)

BadgerDB; Raft; gRPC real — do not implement unless asked. WebRTC DataChannel + screen fake frames exist (`BuildWebRTCHandler`, `BuildScreenHandler`).
