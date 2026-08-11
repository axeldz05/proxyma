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
* **Continuous granularity (3 tiers)**: Unix: `DialUnix` → Write/Read/Scan → `sendUnixSocketCommand` / `dispatchUnixOrLocal` / `dispatchUnixLocalOrOffline` / `dispatchUnixStreamOrLocal`. Same shape for gossip (`enqueueOutbox` → `notifyWithOutbox` → `notifyService`) and tests (`InitClusterCA` → `IssueNode` → `NewNodeTLS`).
* **Tables over switches**: gossip kind → `catalogKinds`; endpoint auth → `httpRoutes` / `routeAuth`; service type → `protocol.serviceTypeSpecs` + `compute.serviceTypeBuilders`. Never add a parallel `switch`.
* **Node addresses**: `protocol.SchemeAddr` (L1) / `HTTPSAddr` / `HTTPSAddrPort` (IPv6-safe); never concatenate `scheme://host:port`. Peer virtual hosts: `PeerLocalHost` / `ParsePeerLocalHost` / `PeerHTTPURL` / `PeerHTTPSURL`.
* **Service query / HTTP acks**: `QueryService` + `WithServiceQuery`; `APIMessage` / `APIStatus` / `APITaskAck` + `utils.RespondMessage` / `RespondStatus`.
* **Leave/Offline**: `protocol.PeerIDRequest` on `PeerClient` (same as HTTP handlers).
* **Error wording**: validation text lives in `protocol/errors.go`; validators may stay per-layer, the message must not be retyped.
* **No parallel maps on one key**: per-entity state goes in one struct under one lock (`peerState`), not N maps with N mutexes.
* **No `panic` / `os.Exit` outside `main`/`Execute`**: CLI uses `RunE`; libraries return errors (`storage.NewStorageEngine` and `server.New` return `(T, error)`); Bolt failures propagate instead of collapsing into a bool.
* **Long-lived goroutines re-check their lifetime after blocking network calls** before logging or firing callbacks (`QUICManager.lifetime`, `NATMapper.stopped`).
* **Never mutate a `tls.Config` already in use**; swap snapshots behind `GetConfigForClient` / `GetClientCertificate`.
* Store is **`go.etcd.io/bbolt`**, never `github.com/boltdb/bolt` (unmaintained, writes past an allocation, aborts under `-race`).
* **UI hints**: `protocol.InferUIHint` / `EffectiveUIHint`; Android uses `FormParameter.isFilePicker()` — no name sniffing.
* **Bind errors**: `BindErrorJSON` / `ParseBindError` / `IsBindError` only (no double-wrap, no `"error:"` prefix).
* **In-memory registry**: Persist `services.json` only on mutation; runtime via `Compute.GetService` / `GetHandler`.
* **No unrequested commits**.

---

## Repository Map (Current)

### CLI — `cmd/proxyma/`
* `root.go` — Cobra from `uischema.VisibleRegistry("cli")` + `Execute`; tables via `uischema.ProjectRows`.
* `cli_actions.go` — `executeActionLocal` → Normalize→Validate → `cliEscapes` OR **`InvokeDomainActionPrepared`**.
* `cli_open.go` — editor + system open; byte/rate rendering calls `uischema.FormatBytes` / `FormatRate` directly.
* `service_help.go` — `ParseInputsToJSON` → `uischema.NormalizePayloadJSON`.

### Bindings — `cmd/proxyma-bind/`
* L1 IPC + L3 **`InvokeDomainAction`** / L2 **`InvokeDomainActionPrepared`** / `NormalizeActionArgs` / **`ValidateActionArgs`** / **`uischema.NormalizePayloadJSON`** / `dispatchUnixOrLocal` / `dispatchUnixStreamOrLocal`.
* **`offlineHooks`** map in `invoke.go` (service.add/remove/detail → compute L2); not inside `unixHandlers`.
* Socket via **`protocol.UnixSockPath`**; `ParameterDetail` = `uischema.ParameterDetail`.
* Execution SSOT: `server.CallUnixUnary` (same bodies as unix listener).
* `LocalServiceDetail` via bind schema paths; `BindErrorJSON` / `IsBindError` on `StartNode`.
* `GetServiceSchema` offline arm; `resolveServiceSchema` / `GetServiceDetails`; `RunPipeline` → `RunService`.

### Server — `internal/server/`
* `server.go` lifecycle; `peers.go` topology; `advertisedTCPPort` / `configTCPPort` (`protocol.DefaultTCPPort`).
* `unix_handlers.go` — **`unixHandlers`** map keyed by `uischema.MustUnixAction` + **`requireUnixArgs`**; `unix_listener.go` accept loop only.
* `handlers.go` — **`httpRoutes`** table (method, path, handler, `authMode`, `RelayAnon`); `mTLSGuard` → `routeAuth`, `HandleRelayForward` → `relayAllowsAnonymous`; unknown path ⇒ `authMTLS`. **`routeIndex`** memoizes policy with `sync.Once` (policy only, never handlers); subtree paths keep the default.
* `registry.go` — **`PeerRegistry`** = one `map[string]*peerState` + one `RWMutex`. **`hasRecord`** is the registration proof used by `mTLSGuard` / relay / `cluster_handlers`; map presence is not.
* `catalog_kinds.go` — `catalogKinds` (`Kind` + `syncOnJoin` + `deliver`) / `catalogKindFor` / `syncCatalogToPeer` / `lookupCachedServiceSchema` / `resolveServiceBidTarget`; `outbox.go` → **`notifyWithOutbox`**.
* `nat.go` — `determineSponsorAndNATStatus` orchestrates `openUDPEndpoint` / `mapPortsWithUPnP` / `startDirectQUIC` / `detectPublicReachability` / `applySponsorStatus`.
* `relay.go` — **`rejectOversizedRelay`**; `tls_rotation.go` / `cluster_handlers.go` — **`protocol.RotateTLSPayload`** (key never travels).
* `service_catalog.go` — Detail/Discover/Add/Remove, **`applyServiceAction`**, `NotifyService*`.
* `service_exec.go` — Run/Stream + ingest (`ResultLocalPath`); **`submitTrackedTask`**.
* `applyPipelineAction` / `NotifySchema*`; `ValidatePipelineSchema` → `protocol.ValidatePipelineSchema` / `PipelineHasCycle`; `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer` + **`gossipToPeer` / `gossipAll`** + `PeerRPC*`.
* `peerCNFromRequest` / `requirePeerCNMatchesBodyID`; HTTP mounts use **`protocol.Path*`** (`handlePeerIDAction`, schema notify re-validates).
* `EstimateTaskCost` / `selectBestServiceBid`; relay **`OriginPeerID`** + response body cap.

### P2P — `internal/p2p/`
* `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr`, `CAKeyPath`, `CACertPaths`, `NodeCertPaths`, `ReadCAPEM` / `ResolveNodeCertPaths`, `PeerCNFromTLS` / `VerifyTLSPeerCN`, `newNodeCertTemplate`, `signLeaf`, `LeafDNSNames`, `CSRCommonName`, `NewHTTPClient`, `PostJSONAbsolute`, `ForwardRelay`, `NewRelayRequest`, `FlattenHTTPHeader`, `LoadNodeTLS`.
* `HashCertDER` / `CAHashFromPEM` / `TLSConfigTrustCAHash` / `WriteNodePEMs`.
* `NATMapper.SetOnMapped`; `HolePunchPingPayload` / `ParseHolePunchPing` / `BurstPings`; `routeOverQUICSession`.
* **`RequireHTTPStatus`** / **`OpenHTTPBody`** for non-`doJSON` paths; `QUICManager` takes a typed `Logger` and accepts on its shutdown ctx; `RoundTrip` = `tryDirectAddresses` → `tryRelay`.

### Storage / Compute / Protocol
* `UpsertAndSubscribe` / `deleteBlobIfOrphan`; bolt JSON + `boltPutFlag` / `boltHasKey`; bucket names in `storage/buckets.go` (`allBuckets`).
* `utils.WriteNDJSON` / `PumpJSON*` / `ForEachNDJSON` / `ScanNDJSON`; `ReadJSONFile` / `WriteJSONFile`.
* `compute.EstimateTaskCost`; `protocol.Path*` / `PathRel` / `MaxRelayBodyBytes`, `RPCTimeout*`, **`DialTimeout*` / `HolePunch*` / `HandlerDial*`**, `DefaultTCPPort`, **`DefaultInviteMinutes`**, **`SockFileName` / `UnixSockPath`**, **`ValidatePipelineSchema` / `PipelineHasCycle`**, `NormalizeServiceSchema`, `DescribeParameter`, `MissingRequired`, `ValidateValue(+Options)`, `ActionAdd`/`Remove`, `ResultLocalPath`, `VFSURI` / `IsStageableLocalPath` / `RewriteLocalFilePaths` / `InferUIHint` / `IsFilePickerHint`, `RelayRequest.OriginPeerID`.
* `protocol` layout: `service_types.go` (`serviceTypeSpecs`), `addr.go` (`SchemeAddr`/`HTTPSAddr`/`PeerLocal*`), `errors.go` (validation wording), `config.go`, `logring.go` — `protocol.go` keeps only types.
* `compute`: `serviceTypeBuilders` → `BuildHandler` (`BuildHTTPHandler`; `BuildGRPCHandler` legacy alias); `withHandlerTimeout` / `streamHTTPClient` / `requireHTTPExec` / `utils.HTTPErrorFromResponse`.
* `storage`: `VFS.Upsert` returns `(bool, error)`; write through `StorageEngine.upsertIndex`; bbolt types are `bbolt.Tx` / `bbolt.DB`.
* `internal/testutil/cluster.go`: `NewStorageEngine` / `InitClusterCA` / `IssueNode` / `NewNodeTLS` / **`InsecureTLSConfig` / `InsecureHTTPClient`** — but tests whose subject *is* a TLS/storage step keep using the L1.
* **Admin UI SSOT**: `shared/uischema.Registry` (`UnixAction`, `Hidden`, `VisibleRegistry`, `FindAction`, **`ValidateActionArgs`**, **`NormalizePayloadJSON`**, **`ProjectRows`/`FormatBytes`/`BandwidthStatsRows`**, shared `vfsNameParam`/`svcNameParam`/`pipelineIDParam`). Compute `ServiceSchema` remains a separate contract.

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
| Gossip | `gossipToPeer` / `gossipAll` / `syncCatalogToPeer` / `catalogKinds` / `catalogKindFor` |
| Notify outbox | `notifyWithOutbox` (L2) / `enqueueOutbox` / `flushOutbox` / `OutboxPendingCount` (Bolt `notify_outbox`) |
| Endpoint + auth policy | `httpRoutes` / `routeIndex` / `routeAuth` / `relayAllowsAnonymous` |
| Per-peer state | `peerState` in `registry.go` (one lock; `hasRecord` = registered) |
| Validation error wording | `protocol.ParamTypeError` / `ParamOptionError` / `MissingParamError` / `ErrEmptyPipelineID` |
| Service type alias / streaming / builder | `protocol.serviceTypeSpecs` / `compute.serviceTypeBuilders` |
| Handler timeout / stream client / exec check | `withHandlerTimeout` / `streamHTTPClient` / `requireHTTPExec` |
| Node URL (IPv6-safe) | `protocol.SchemeAddr` / `HTTPSAddr` / `HTTPSAddrPort` |
| Peer `.proxyma.local` URL | `protocol.PeerLocalHost` / `ParsePeerLocalHost` / `PeerHTTPURL` / `PeerHTTPSURL` |
| Service `?service=` query | `protocol.QueryService` / `WithServiceQuery` |
| HTTP ack message/status | `protocol.APIMessage` / `APIStatus` / `APITaskAck`; `utils.RespondMessage` / `RespondStatus` |
| Leave/Offline RPC body | `protocol.PeerIDRequest` |
| Unexpected HTTP status | `utils.HTTPStatusError` / `HTTPErrorFromResponse` / `p2p.RequireHTTPStatus` / `OpenHTTPBody` |
| Required unix args / relay cap | `requireUnixArgs` / `rejectOversizedRelay` |
| TLS rotation payload | `protocol.RotateTLSPayload` |
| Test storage / CA / node TLS | `testutil.NewStorageEngine` / `InitClusterCA` / `IssueNode` / `NewNodeTLS` / `InsecureTLSConfig` / `InsecureHTTPClient` |
| Service subscribe | `LocalServiceSubscribe` / `SetServiceSubscription` / `IsServiceSubscribed` / `MatchServicePattern` |
| Bid strategy | `NormalizeSortStrategy` / `LocalServiceRun(..., strategy)` / `--strategy` |
| OTel bid export | `telemetry.ExportBidAsync` / `InitFromEnv` / `SetBidExporter` |
| Pipeline/service apply | `applyPipelineAction` / `applyServiceAction` / `submitTrackedTask` |
| Service gossip | `NotifyService` / `NotifyServiceToPeer` |
| Blob fetch / CAS | `fetchBlobFromPeer` / `SaveVerifiedPhysicalBlob` / `UpsertAndSubscribe` / `deleteBlobIfOrphan` / `IsValidCASHash` / `StageAndRewrite` / `protocol.RewriteLocalFilePaths` |
| VFS local ops | `LocalVFSUpload` / `ResolveLocalBlob` / `StageLocalFile` / `StageAndRewrite` |
| VFS URI | `protocol.VFSURI` / `ParseVFSURI` / `IsStageableLocalPath` |
| HTTP paths / relay | `protocol.Path*` / `QueryService` / `WithServiceQuery` / `NewRelayRequest` / `RequestPathWithQuery` / `MaxRelayBodyBytes` |
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
