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
* `root.go` — Cobra from `uischema.VisibleRegistry("cli")` + `Execute`; tables via `uischema.ProjectRows`.
* `cli_actions.go` — `executeActionLocal` → Normalize→Validate → `cliEscapes` OR **`InvokeDomainActionPrepared`**.
* `cli_render.go` / `cli_open.go` — `uischema.FormatBytes` wrappers / editor+open.
* `service_help.go` — `ParseInputsToJSON` → `uischema.NormalizePayloadJSON`.

### Bindings — `cmd/proxyma-bind/`
* L1 IPC + L3 **`InvokeDomainAction`** / L2 **`InvokeDomainActionPrepared`** / `NormalizeActionArgs` / **`ValidateActionArgs`** / **`uischema.NormalizePayloadJSON`** / `dispatchUnixOrLocal` / `dispatchUnixStreamOrLocal`.
* **`offlineHooks`** map in `invoke.go` (service.add/remove/detail → compute L2); not inside `unixHandlers`.
* Socket via **`protocol.UnixSockPath`**; `ParameterDetail` = `uischema.ParameterDetail`.
* Execution SSOT: `server.CallUnixUnary` (same bodies as unix listener).
* `LocalServiceDetail` via bind schema paths; `BindErrorJSON` / `IsBindError` (StartNode/ChangeStorage too).
* `GetServiceSchema` offline arm; `resolveServiceSchema` / `GetServiceDetails`; `RunPipeline` → `RunService`.

### Server — `internal/server/`
* `server.go` lifecycle; `peers.go` topology; `advertisedTCPPort` / `configTCPPort` (`protocol.DefaultTCPPort`).
* `unix_handlers.go` — **`unixHandlers`** map keyed by `uischema.MustUnixAction`; `unix_listener.go` accept loop only.
* `catalog_kinds.go` — `catalogKinds` / `syncCatalogToPeer` / `lookupCachedServiceSchema` / `resolveServiceBidTarget`.
* `service_catalog.go` — Detail/Discover/Add/Remove, **`applyServiceAction`**, `NotifyService*`.
* `service_exec.go` — Run/Stream + ingest (`ResultLocalPath`); **`submitTrackedTask`**.
* `applyPipelineAction` / `NotifySchema*`; `ValidatePipelineSchema` → `protocol.ValidatePipelineSchema` / `PipelineHasCycle`; `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer` + **`gossipToPeer` / `gossipAll`** + `PeerRPC*`.
* `peerCNFromRequest` / `requirePeerCNMatchesBodyID`; HTTP mounts use **`protocol.Path*`** (`handlePeerIDAction`, schema notify re-validates).
* `EstimateTaskCost` / `selectBestServiceBid`; relay **`OriginPeerID`** + response body cap.

### P2P — `internal/p2p/`
* `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr`, `CAKeyPath`, `CACertPaths`, `NodeCertPaths`, `ReadCAPEM` / `ResolveNodeCertPaths`, `PeerCNFromTLS` / `VerifyTLSPeerCN`, `newNodeCertTemplate`, `signLeaf`, `LeafDNSNames`, `CSRCommonName`, `NewHTTPClient`, `PostJSONAbsolute`, `ForwardRelay`, `NewRelayRequest`, `FlattenHTTPHeader`, `LoadNodeTLS`.
* `HashCertDER` / `CAHashFromPEM` / `TLSConfigTrustCAHash` / `WriteNodePEMs`.
* `NATMapper.SetOnMapped`; `HolePunchPingPayload` / `ParseHolePunchPing` / `BurstPings`; `routeOverQUICSession`.

### Storage / Compute / Protocol
* `UpsertAndSubscribe` / `deleteBlobIfOrphan`; bolt JSON + `boltPutFlag` / `boltHasKey`; bucket names in `storage/buckets.go` (`allBuckets`).
* `utils.WriteNDJSON` / `PumpJSON*` / `ForEachNDJSON` / `ScanNDJSON`; `ReadJSONFile` / `WriteJSONFile`.
* `compute.EstimateTaskCost`; `protocol.Path*` / `PathRel` / `MaxRelayBodyBytes`, `RPCTimeout*`, **`DialTimeout*` / `HolePunch*` / `HandlerDial*`**, `DefaultTCPPort`, **`DefaultInviteMinutes`**, **`SockFileName` / `UnixSockPath`**, **`ValidatePipelineSchema` / `PipelineHasCycle`**, `NormalizeServiceSchema`, `DescribeParameter`, `MissingRequired`, `ValidateValue(+Options)`, `ActionAdd`/`Remove`, `ResultLocalPath`, `VFSURI` / `IsStageableLocalPath` / `RewriteLocalFilePaths` / `InferUIHint` / `IsFilePickerHint`, `RelayRequest.OriginPeerID`.
* **Admin UI SSOT**: `shared/uischema.Registry` (`UnixAction`, `Hidden`, `VisibleRegistry`, `FindAction`, **`ValidateActionArgs`**, **`NormalizePayloadJSON`**, **`ProjectRows`/`FormatBytes`/`BandwidthStatsRows`**). Compute `ServiceSchema` remains a separate contract.

### Bindings / Android
* `LookupServiceSchema`→`resolveServiceSchema`, `ResolveLocalBlob`, `ResolveTaskResultPath`; CLI uses PersistentFlag `cliStorage` only.
* Android-as-interpreter (when UI returns): `VisibleRegistry` + **`InvokeDomainAction`** + `ProjectRows`; typed JNI wrappers optional; no hardcoded admin `domain.action` screens. Trust bind `uiHint`.

### Examples — `services-examples/`
* Lab services: `sensor.telemetry` (server_stream), `music.resolve`/`convert`/`stream`, `remote.screen`/`input`, `media.resize`/`watermark`, `clipboard.sync`, `shell.attach`.
* Pipelines: `music_prepare_pipeline.json`, `media/thumbnail_pipeline.json`, `ocr_obsidian_pipeline.json`.
* `*_service.json` uses `__SERVICES_DIR__`; `scripts/bootstrap_dev.sh` globs + rewrites + registers all services/pipelines.
* Editor: module `proxyma` package `./services-examples/editor` (`protocol` types + `dialUnary`).
* `start_upstreams.sh` for HTTP NDJSON upstreams (ports 19101/19102); registered by `scripts/bootstrap_dev.sh`.
* Music unit tests: `music/test_music.py`. See `services-examples/README.md`.

---

## What Already Exists (Do Not Reinvent)

| Need | Use |
|------|-----|
| services.json | `compute.Load/Save/Build/Upsert/Delete*` |
| Schema resolve | `LocalServiceDetail` / `LookupServiceSchema` / `GetServiceSchema` |
| Schema normalize | `NormalizeServiceSchema` / `DescribeParameter` / `ActionAdd` |
| UI hint | `InferUIHint` / `EffectiveUIHint` / `IsFilePickerHint` / `IsImagePickerHint` |
| Missing required / admin validate | `protocol.MissingRequired` / `uischema.ValidateActionArgs` |
| Table projection / bytes | `uischema.ProjectRows` / `FormatBytes` / `BandwidthStatsRows` |
| Payload JSON / arg normalize | `uischema.NormalizePayloadJSON` / `NormalizeActionArgs` |
| Invite TTL | `protocol.DefaultInviteMinutes` |
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
| RPC / dial / handler timeouts | `protocol.RPCTimeout*` / `DialTimeout*` / `HolePunch*` / `HandlerDial*` / `PeerRPC*` |
| Default TCP port | `protocol.DefaultTCPPort` |
| Unix sock path | `protocol.SockFileName` / `UnixSockPath` |
| Pipeline validate / cycle | `protocol.ValidatePipelineSchema` / `PipelineHasCycle` |
| Bolt bucket names | `storage` `allBuckets` / `bucket*` / `vfsIndexBucket` |
| Admin UI actions | `uischema.Registry` / `UnixAction` / `FindAction` / `VisibleRegistry` / `SuccessMessage` |
| Unix/CLI dispatch | `server.CallUnixUnary` / `InvokeDomainAction` / `InvokeDomainActionPrepared` / `offlineHooks` / `cliEscapes` |
| Unix IPC | Dial/Write/Read/Scan + `dispatchUnixOrLocal` / `dispatchUnixLocalOrOffline` / `dispatchUnixStreamOrLocal` |
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
1. `LocalInviteGenerate` → `(token, expires)` + `protocol.DefaultInviteMinutes`.
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
