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
* **Continuous granularity (3 tiers)**: Unix: `internal/unixclient` Dial/Write/Read/Scan → `CallUnary` → bind dispatch/editor `dialUnary`. Same shape for gossip (`enqueueOutbox` → `notifyWithOutbox` → `notifyService`) and tests (`InitClusterCA` → `IssueNode` → `NewNodeTLS`).
* **Tables over switches**: gossip kind → `catalogKinds`; endpoint auth → `httpRoutes` / `routeAuth`; service type → `protocol.serviceTypeSpecs` + `compute.serviceTypeBuilders`. Never add a parallel `switch`.
* **Node addresses**: `protocol.SchemeAddr` (L1) / `HTTPSAddr` / `HTTPSAddrPort` (IPv6-safe); never concatenate `scheme://host:port`. Peer virtual hosts: `PeerLocalHost` / `ParsePeerLocalHost` / `PeerHTTPURL` / `PeerHTTPSURL`.
* **Service query / HTTP acks**: `QueryService` + `WithServiceQuery`; `APIMessage` / `APIStatus` / `APITaskAck` + `utils.RespondMessage` / `RespondStatus`.
* **Leave/Offline**: `protocol.PeerIDRequest` on `PeerClient` (same as HTTP handlers).
* **AddPeer RPC**: `protocol.AddPeerRequest` on `PeerClient.AddPeer` (same as Announce/HTTP).
* **Missing mTLS**: `p2p.ForbidMissingMTLS` / `RequirePeerCN`; server wrappers only add package-local ergonomics.
* **Error wording**: validation text lives in `protocol/errors.go`; validators may stay per-layer, the message must not be retyped.
* **No parallel maps on one key**: per-entity state goes in one struct under one lock (`peerState`), not N maps with N mutexes.
* **No `panic` / `os.Exit` for recoverable runtime failures outside `main`/`Execute`**: CLI uses `RunE`; `storage.NewStorageEngine`, `compute.NewComputeEngine`, and `server.New` return `(T, error)`. Narrow init-only invariants may use `Must*` helpers or unexported static-data decoders (`MustUnixAction`, `mustDecodeB64`); I/O, configuration, and request failures must propagate errors.
* **Long-lived goroutines re-check their lifetime after blocking network calls** before logging or firing callbacks (`QUICManager.lifetime`, `NATMapper.stopped`).
* **Never mutate a `tls.Config` already in use**; swap snapshots behind `GetConfigForClient` / `GetClientCertificate`.
* Store is **`go.etcd.io/bbolt`**, never `github.com/boltdb/bolt` (unmaintained, writes past an allocation, aborts under `-race`).
* **UI hints**: `protocol.InferUIHint` / `EffectiveUIHint`; Android uses `FormParameter.isFilePicker()` — no name sniffing.
* **Bind errors**: `BindErrorJSON` / `ParseBindError` / `IsBindError` only (no double-wrap, no `"error:"` prefix).
* **In-memory registry**: Persist `services.json` only on mutation; runtime via `Compute.GetService` / `GetHandler`.
* **No unrequested commits**.

---

## Remediation Contracts (Current)

* **Server lifetime**: `Ready`/`IsReady` mean both Unix and TCP listeners are bound. Server-owned goroutines and in-process calls join the shutdown barrier through `goOwned` / `AcquireWorkLease`; HTTP and Unix work is tracked too. Shutdown stops admission, cancels `lifetimeCtx`, closes listeners/connections, joins NAT/listener/work/download/compute/router/QUIC/storage owners, and only then closes `ShutdownDone`.
* **Compute lifetime**: construct with `compute.NewComputeEngine(parentCtx, ...)` and handle its error (a nil parent is rejected). Its private `lifetimeCtx` / `cancelLifetime` own workers and derived RPC/handler timeouts; parent cancellation closes the engine. Request contexts still travel as method arguments—never store a request context on `ComputeEngine`.
* **NAT / QUIC / router**: access published NAT state through `CurrentNATState` / `natMu`; schedule and join setup with `beginNATWork` / `stopNATWork`. `NATMapper.Start` / `WaitReady` / `Stop` is one synchronized lifetime. Router prewarms are cancellation- and generation-bound; route removal blocks late QUIC sessions, and `PeerClient.Close` joins router work.
* **Relay**: queue capacity and `RelayManager.workSlots` bound pending and active work. Decode through `decodeRelayJSON`; enforce `MaxRelayBodyBytes` on requests and the capped response writer. Poll/request work must remain server-owned and lifetime-cancelable.
* **Durable mutation ordering**: outbox v2 uses separate entry/generation/reservation buckets. `notifyWithOutbox` stages a generation before send and compare-deletes only the acknowledged bytes; `prepareOutboxMutation` stages gossip before VFS/pipeline commit and reconciles on commit/rollback. Legacy migration moves only structurally recognized metadata. Download work (`download_intents`), orphan cleanup (`pending_blob_gc`), and pending invites (`pending_invites`) survive restart. Invite mint persists before returning a token; consume deletes durably before CSR signing and restores on recoverable signing/CA-read failure.
* **Storage integrity / manifests**: server paths use error-preserving APIs (`GetFileMetaE`, `IsSubscribedE`, `HasServiceSubscriptionsE`, `IsServiceSubscribedE`, `ProcessRemoteManifestE`). Manifest reconciliation processes every entry in deterministic name order, preserves successful decisions, aggregates per-entry errors, and `ProcessRemoteManifestFromSource` still stages durable intents for every successful missing entry. VFS ordering is total: version, tombstone, hash, size, name. Blob writes stage/verify outside the metadata lock, then commit; remote content is accepted only when SHA-256 and the current name/version/hash/live tuple match.
* **`services.json`**: use compute Load/Save/Upsert/Delete helpers only. They combine a canonical-path in-process RW lock, Linux/Android advisory `flock`, and atomic `utils.WriteJSONFile` (temp + fsync + rename + parent fsync).
* **Pipelines / tasks**: `PipelineSchema.Version`, `Deleted`, `PipelineSchemaHash`, `ValidatePipelineRevision`, and `ApplyPipelineRevision` define revision/tombstone integrity. `PipelineExecutionState` is engine-owned and binds ID/version/hash/current step/outputs/producers plus pinned `selected_targets`; continuations require `CapabilityPipelineState` **v2**. Authenticated producer/delegate checks guard callbacks. Task status transitions are `pending` → `ingesting` when output import is required → `completed`, or `failed`; terminal states do not regress and retention is bounded.
* **Streams**: HTTP peers and Unix clients explicitly negotiate legacy or v1 framing. Legacy carries raw object chunks and ends successfully at EOF; v1 uses `ServiceStreamFrame` / versioned `UnixResponse` chunk/error/complete frames and requires a terminal frame. Never guess control records or accept mixed legacy/v1 framing. Cancellation propagates from bind `CancelStream` through contexts, and every NDJSON path uses `utils.MaxNDJSONFrameBytes`.
* **Bind runtime**: the singleton node runtime is guarded by `srvMutex`; `StartNode` publishes it only after `Server.Ready`, then starts bootstrap immediately as announce (which waits on NAT setup) → discovery → relay polling, without fixed startup sleeps. `IsNodeRunning` requires readiness, and `StopNodeWithError` waits for finalization while leaving timed-out state marked stopping. Join installation is a journaled stage/backup/swap transaction recovered before start. Capture one `canonicalStoragePath` per operation, and normalize Unix unavailability separately from daemon/application errors so offline fallback cannot mask a live daemon failure.
* **gomobile / Android**: keep exported gomobile signatures stable. `make test-android-contract` uses the host-only `androidcontract` tag to select Android WebRTC stubs: registration fails immediately and signaling returns the exact JSON HTTP 501 unsupported contract. Hermetic `make test-android` depends on it, resolves the module-pinned `gobind` with `go tool -n gobind` instead of `gomobile init`/`@latest`, builds one temporary fresh AAR, verifies Java descriptors including the UISchema JSON bridge, isolates Android user state, and passes that exact AAR to Kotlin unit tests, lint, and assemble; the dedicated CI Android job provisions pinned Go/Java/SDK/NDK/Gradle inputs and runs this target. Pion helpers exist only on `!android && !androidcontract`. Compose work belongs to `ViewModel`/`viewModelScope`, cancellable streams own a `StreamLease`, `ProxymaService` owns daemon start/stop, and UI binding state must unbind on Activity destruction.
* **Accepted trust/network limits**: enrolled mTLS peers and explicitly selected pipeline delegates are trusted; capability/provenance fields are authorization metadata, not cryptographic proofs. Mixed-version distributed pipeline continuations are rejected unless the peer supports pipeline-state v2. There is no TURN fallback, and UPnP/NAT-PMP mapping is IPv4-only.
* **Race gate**: H1 (live TLS mutation) and H2 (NAT-after-shutdown) are closed. A full `go test -race ./...` / `make test-race` is required; do not preserve a “known race failure” exemption.

---

## Repository Map (Current)

### CLI — `cmd/proxyma/`
* `root.go` — Cobra from **`cliRegistry()`** (`VisibleRegistry("cli")` + Hidden CLI escapes) + `Execute`; tables via `uischema.ProjectRows`.
* `cli_actions.go` — `executeActionLocal` → Normalize→Validate → `cliEscapes` OR **`InvokeDomainActionPrepared`**.
* `cli_open.go` — editor + system open; byte/rate rendering calls `uischema.FormatBytes` / `FormatRate` directly.
* `service_help.go` — `ParseInputsToJSON` → `uischema.NormalizePayloadJSON`.
* `cli_daemon_test.go::TestCLIContractLiveDaemon` builds the real CLI and drives a real bind daemon over Unix IPC for upload help/default+explicit names, list, telemetry, and clean shutdown.

### Bindings — `cmd/proxyma-bind/`
* L1 IPC + L3 **`InvokeDomainAction`** / L2 **`InvokeDomainActionPrepared`** / `NormalizeActionArgs` / **`ValidateActionArgs`** / **`uischema.NormalizePayloadJSON`** / `dispatchUnixOrLocal` / `dispatchUnixStreamOrLocal`.
* **`offlineHooks`** map in `invoke.go` (service.add/remove/detail → compute L2); not inside `unixHandlers`.
* Socket via **`protocol.UnixSockPath`**; `ParameterDetail` = `uischema.ParameterDetail`.
* Execution SSOT: `server.CallUnixUnary` (same bodies as unix listener).
* `LocalServiceDetail` via bind schema paths; `BindErrorJSON` / `IsBindError` on `StartNode`.
* `GetServiceSchema` offline arm; `resolveServiceSchema` / `GetServiceDetails`; `RunPipeline` → `RunService`.

### Server — `internal/server/`
* `server.go` lifecycle (`Ready`, `goOwned`, `AcquireWorkLease`, `ShutdownDone`; shutdown joins every owner even if HTTP shutdown fails); `peers.go` topology; `advertisedTCPPort` / `configTCPPort` (`protocol.DefaultTCPPort`).
* `unix_handlers.go` — **`unixHandlers`** map keyed by `uischema.MustUnixAction` + **`requireUnixArgs`**; `unix_listener.go` accept loop only.
* `handlers.go` — **`httpRoutes`** table (method, path, handler, `authMode`, `RelayAnon`); `mTLSGuard` → `routeAuth`, `HandleRelayForward` → `relayAllowsAnonymous`; unknown path ⇒ `authMTLS`. Protected routes require self or an explicit `hasRecord` peer, with no literal-CN bypass; only announce uses `authMTLSUnregistered`. **`routeIndex`** memoizes policy with `sync.Once` (policy only, never handlers); subtree paths keep the default.
* `registry.go` — **`PeerRegistry`** = one `map[string]*peerState` + one `RWMutex`. **`hasRecord`** is the registration proof used by `mTLSGuard` / relay / `cluster_handlers`; map presence is not. **`AddPeer` → `(updated, evicted)`**; equal-seq merge keeps `Addresses[0]`.
* `invite.go` — durable **`Check`** / **`CheckAndConsume`** over storage `pending_invites`; join validates first, consumes atomically before `SignCSR`, and restores durably on recoverable signing/CA-read failure.
* `catalog_kinds.go` — `catalogKinds` (`Kind` + `entityFrom` + `current` + `syncOnJoin` + `deliver`) / `catalogKindFor` / `syncCatalogToPeer` / `lookupCachedServiceSchema` / `resolveServiceBidTarget`; eager and retry outbox paths both deliver through the table.
* `nat.go` — `determineSponsorAndNATStatus` orchestrates `openUDPEndpoint` / `mapPortsWithUPnP` / `startDirectQUIC` / `detectPublicReachability` / `applySponsorStatus`.
* `relay.go` — **`rejectOversizedRelay`**; `tls_rotation.go` / `cluster_handlers.go` — **`protocol.RotateTLSPayload`** (key never travels).
* `service_catalog.go` — Detail/Discover/Add/Remove, **`applyServiceAction`**, `NotifyService*`.
* `service_exec.go` — Run/Stream + ingest (`ResultLocalPath`); **`submitTrackedTask`**.
* `applyPipelineAction` / `NotifySchema*`; `ValidatePipelineSchema` → `protocol.ValidatePipelineSchema` / `PipelineHasCycle`; `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer` + **`errPeerSkipped`** (no liveness update) + **`gossipToPeer` / `gossipAll`** + `PeerRPC*`; `forEachPeerOpts.Context` binds fan-out to its owner.
* `peerCNFromRequest` / `requirePeerCNMatchesBodyID`; **`HandleAddPeer`** requires CN (owner sticky IsSponsor only; gossip cannot elevate); schema/hole-punch CN-bound like services; HTTP mounts use **`protocol.Path*`**.
* Invites/rotate: **`hasCAKey`** gates mint; join **`CheckAndConsume`**; rotate requires peer push acks before local TLS reload.
* `vfs_sync.downloadWorker` — fallback skips source peer; **`ErrBlobDiscarded`** → debug only (callPeer keeps peer online).
* VFS: **`Snapshot() (map, error)`** — orphan GC aborts on error; `physical.Storage` by pointer; stage fail-closed on missing local paths.
* `EstimateTaskCost` / `selectBestServiceBid`; relay **`OriginPeerID`** + response body cap; relay reply CN must match target.

### P2P — `internal/p2p/`
* `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr`, `CAKeyPath`, `CACertPaths`, `NodeCertPaths`, `ReadCAPEM` / `ResolveNodeCertPaths`, `PeerCNFromTLS` / `VerifyTLSPeerCN`, **`ForbidMissingMTLS` / `RequirePeerCN`**, `newNodeCertTemplate`, `signLeaf`, `LeafDNSNames`, `CSRCommonName`, `NewHTTPClient`, `PostJSONAbsolute`, `ForwardRelay`, `NewRelayRequest`, `FlattenHTTPHeader`, `LoadNodeTLS`.
* `HashCertDER` / `CAHashFromPEM` / `TLSConfigTrustCAHash` / `WriteNodePEMs`.
* `NATMapper.SetOnMapped`; `HolePunchPingPayload` / `ParseHolePunchPing` / `BurstPings`; `routeOverQUICSession`.
* **`RequireHTTPStatus`** / **`OpenHTTPBody`** for non-`doJSON` paths; `QUICManager` takes a typed `Logger` and accepts on its shutdown ctx; `RoundTrip` = `tryDirectAddresses` → `tryRelay`.

### Storage / Compute / Protocol
* `UpsertAndSubscribe` (returns error) / `ErrBlobDiscarded` / `deleteBlobIfOrphan`; bolt JSON + `boltPutFlag` / `boltHasKey`; bucket names in `storage/buckets.go` (`allBuckets`).
* Physical CAS: `BlobExists` Stats disk when cache hits; `DeleteBlob` clears cache on `IsNotExist` (idempotent).
* `utils.WriteNDJSON` / `PumpJSON*` / `ForEachNDJSON` / `ScanNDJSON`; `ReadJSONFile` / `WriteJSONFile`.
* `compute.EstimateTaskCost`; `protocol.Path*` / `PathRel` / `MaxRelayBodyBytes`, `RPCTimeout*`, **`DialTimeout*` / `HolePunch*` / `HandlerDial*`**, `DefaultTCPPort`, **`DefaultInviteMinutes`**, **`SockFileName` / `UnixSockPath`**, **`ValidatePipelineSchema` / `PipelineHasCycle`**, `NormalizeServiceSchema`, `DescribeParameter`, `MissingRequired`, `ValidateValue(+Options)`, `ActionAdd`/`Remove`, `ResultLocalPath`, `VFSURI` / `IsStageableLocalPath` / `RewriteLocalFilePaths` (returns error) / `InferUIHint` / `IsFilePickerHint`, `RelayRequest.OriginPeerID`.
* `protocol` layout: `service_types.go` (`serviceTypeSpecs`), `addr.go` (`SchemeAddr`/`HTTPSAddr`/`PeerLocal*`), `errors.go` (validation wording), `config.go`, `logring.go` — `protocol.go` keeps only types.
* `compute`: `serviceTypeBuilders` → `BuildHandler` (`BuildHTTPHandler` / `BuildHTTPBidiHandler` / `BuildHTTPServerStreamHandler`); `withHandlerTimeout` / `streamHTTPClient` / `requireHTTPExec` / `utils.HTTPErrorFromResponse`.
* `storage`: `VFS.Upsert` returns `(bool, error)`; write through `StorageEngine.upsertIndex`; bbolt types are `bbolt.Tx` / `bbolt.DB`.
* `internal/testutil/cluster.go`: `NewStorageEngine` / `InitClusterCA` / `IssueNode` / `NewNodeTLS` / **`InsecureTLSConfig` / `InsecureHTTPClient`** — but tests whose subject *is* a TLS/storage step keep using the L1.
* **Admin UI SSOT**: `shared/uischema.Registry` (`UnixAction`, `Hidden`, `VisibleRegistry`, `FindAction`, **`ValidateActionArgs`**, **`NormalizePayloadJSON`**, **`ProjectRows`/`FormatBytes`/`BandwidthStatsRows`**, shared `vfsNameParam`/`svcNameParam`/`pipelineIDParam`). Compute `ServiceSchema` remains a separate contract.

### Bindings / Android
* `LookupServiceSchema`→`resolveServiceSchema`, `ResolveLocalBlob`, `ResolveTaskResultPath`; CLI uses PersistentFlag `cliStorage` only.
* Android registry interpreter: `GetUISchemaJSONForSurface("android")` + **`InvokeDomainActionJSON`** + **`ProjectActionRowsJSON`**; typed JNI wrappers remain ABI-compatible facades. Admin forms/tables use typed UISchema metadata; compute `ServiceDetail` stays separate.

### Examples — `services-examples/`
* Lab services: `sensor.telemetry` (server_stream), `music.resolve`/`convert`/`stream`, `remote.screen`/`input`, `media.resize`/`watermark`, `clipboard.sync`, `shell.attach`.
* Pipelines: `music_prepare_pipeline.json`, `media/thumbnail_pipeline.json`, `ocr_obsidian_pipeline.json`.
* `*_service.json` uses `__SERVICES_DIR__`; `scripts/bootstrap_dev.sh` globs + rewrites + registers all services/pipelines.
* Editor: module `proxyma` package `./services-examples/editor`; `uischema.MustUnixAction` supplies action names and `internal/unixclient` supplies the dial/write/read stack.
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
| Notify outbox | `notifyWithOutbox` / `prepareOutboxMutation` / `enqueueOutbox` / `flushOutbox` / `OutboxPendingCount` (v2 entries + generations + reservations; legacy reconciled) |
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
| Add-peer RPC body | `protocol.AddPeerRequest` on `PeerClient.AddPeer` |
| Missing-mTLS forbid | `p2p.ForbidMissingMTLS` / `RequirePeerCN` (server wrappers are thin) |
| Unexpected HTTP status | `utils.HTTPStatusError` / `HTTPErrorFromResponse` / `p2p.RequireHTTPStatus` / `OpenHTTPBody` |
| Required unix args / relay cap | `requireUnixArgs` / `rejectOversizedRelay` |
| TLS rotation payload | `protocol.RotateTLSPayload` |
| Test storage / CA / node TLS | `testutil.NewStorageEngine` / `InitClusterCA` / `IssueNode` / `NewNodeTLS` / `InsecureTLSConfig` / `InsecureHTTPClient` |
| Service subscribe | `LocalServiceSubscribe` / `SetServiceSubscription` / `IsServiceSubscribed` / `MatchServicePattern` |
| Bid strategy | `NormalizeSortStrategy` / `LocalServiceRun(..., strategy)` / `--strategy` |
| OTel bid export | `telemetry.ExportBidAsync` / `InitFromEnv` / `SetBidExporter` |
| Pipeline/service apply | `applyPipelineAction` / `applyServiceAction` / `submitTrackedTask` |
| Service gossip | `NotifyService` / `NotifyServiceToPeer` |
| Blob fetch / CAS | `fetchBlobFromPeer` / `SaveVerifiedPhysicalBlob` / `UpsertAndSubscribe` / `ErrBlobDiscarded` / `deleteBlobIfOrphan` / `IsValidCASHash` / `StageAndRewrite` (error) / `protocol.RewriteLocalFilePaths` (error) |
| VFS local ops | `LocalVFSUpload` / `ResolveLocalBlob` / `StageLocalFile` / `StageAndRewrite` (error) |
| VFS URI | `protocol.VFSURI` / `ParseVFSURI` / `IsStageableLocalPath` |
| HTTP paths / relay | `protocol.Path*` / `QueryService` / `WithServiceQuery` / `NewRelayRequest` / `RequestPathWithQuery` / `MaxRelayBodyBytes` |
| RPC / dial / handler timeouts | `protocol.RPCTimeout*` / `DialTimeout*` / `HolePunch*` / `HandlerDial*` / `PeerRPC*` |
| Default TCP port | `protocol.DefaultTCPPort` |
| Unix sock path | `protocol.SockFileName` / `UnixSockPath` |
| Pipeline validate / cycle | `protocol.ValidatePipelineSchema` / `PipelineHasCycle` |
| Bolt bucket names | `storage` `allBuckets` / `bucket*` / `vfsIndexBucket` (outbox v2, download intents, pending GC included) |
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
1. `LocalInviteGenerate` → `(token, expires)` + `protocol.DefaultInviteMinutes` (**CA key required**).
2. Join CSR → `/cluster/join` with **`CheckAndConsume`** (or `ForwardRelay`).
3. `mTLSGuard` on inter-node HTTP (`peerCNFromRequest` / `requirePeerCN`).

### VFS / Compute
1. `announceAndSync` + `forEachPeer` manifests → `fetchBlobFromPeer`.
2. Bids via `mapEachPeer`; on-demand via `firstPeer`; dispatch with `StageLocalFile`.

### Testing workflow

[`tests/e2e/README.md`](../tests/e2e/README.md) is the testing contract, current 27-executable/26-stable case matrix, profile map, and README decision-gate ledger.

* `make test` owns unit/package execution, including the real CLI/daemon golden path. `make test-integration` owns live bind Unix-IPC and real mTLS restart contracts. The bind suite includes service/storage actions; Android-facing metadata; bandwidth/log telemetry; enrolled-peer DTOs; stream/cancel; task polling; complete pipeline add/run/list/get/clone/remove lifecycle; and recovery of service, pipeline, VFS, and local blob state across daemon restart.
* `make test-cover` runs an uncached `go test ./...` coverage pass and checks [`scripts/coverage_baseline.json`](../scripts/coverage_baseline.json) through [`cmd/coverage-ratchet`](../cmd/coverage-ratchet). `make test-e2e-harness` checks profile-validator fixtures, repository profile invariants, and diagnostic redaction; `make test-sanitizer` is its compatibility alias. `make test-ci` combines `test-cover`, the harness, and the complete `make test-race` pass; it does not run Docker E2E.
* `make test-android-contract` uses the `androidcontract` host tag: WebRTC registration must fail immediately and signaling must return exact JSON HTTP 501. `make test-android` also builds one fresh AAR and verifies `javap` descriptors for run/result/cancel plus `GetUISchemaJSONForSurface` / `InvokeDomainActionJSON` / `ProjectActionRowsJSON` before Kotlin tests, lint, and assemble.
* `make coverage` runs stable E2E `full` and prints separate unit, E2E, and union reports. E2E coverage is generated by the instrumented `./cmd/proxyma` Docker binary and does not cover bind-only/unlinked paths. Missing E2E `covmeta.*` or `covcounters.*` fails by default; `COVERAGE_ALLOW_MISSING_E2E=true` is only an explicit local unit-only escape.
* [`internal/covermerge`](../internal/covermerge) is the coverage merge SSOT. It validates modes and statement metadata, emits one block per source range, ORs duplicate `set` coverage, and sums `count`/`atomic`; [`scripts/merge_profiles.go`](../scripts/merge_profiles.go) uses strict inputs. The union is not an average of percentages.
* Ratchet `check` fails regressions/missing tracked packages and warns on untracked packages. `update` preserves exclusions and writes one-decimal floors; epsilon is `0.1`. New bind/CLI/Android contracts change measured coverage, so the parent coverage work must regenerate `coverage-unit.out` and run `update` before any new floor or total is documented. Freeze no percentages until then.
* `make test-e2e-pr`, `make test-e2e-network`, and `make test-e2e` / `make test-e2e-full` are the Docker entry points. Use `E2E_CASE=NN ./tests/e2e/run.sh` for one case and `E2E_PROFILE=<name>` for `functional`, `pr`, `network`, `infrastructure`, `smoke`, `quarantine`, `full`, or explicit `all`; no selector defaults to stable `full`.
* No new fixed sleeps. Go tests synchronize with channels/contexts and use deadlines only as failure bounds; shell cases use `wait_until` / `wait_for_output`.
* Functional Docker scenarios are **Given** topology/fixtures/faults, **When** public CLI or mTLS API action, **Then** public output/status/content assertion. Private files, logs, stores, and transport choices are not product assertions.
* There are 27 executable cases: `full` contains 26 stable cases, 13 is quarantined, and 23 remains absent until public bidi supports multi-message input and end-to-end cancel. `functional`/`pr` include 26–28; `network` includes 25. Case 01 validates the OCR PDF download against manifest SHA-256, non-empty size, and `%PDF-` magic; 25 covers peer offline/recovery; 26 VFS peer restart; 27 unsubscribe/purge/resubscribe/open; 28 service removal across restart.
* Add every case explicitly to profiles. Every script uses `set -euo pipefail`, one unique static `E2E_PROJECT_NAME`, shared `install_e2e_case_trap`, and cleanup before setup. `scripts/validate_e2e_profiles.sh` rejects invalid/ambiguous/duplicate selectors, non-executables, project-name collisions, stable/quarantine overlap or orphaning, and `pr`/`functional` drift.
* Preserve blocked decisions: the suite does not green ambiguous dynamic-port behavior, passive sync without an SLO, real UPnP/NAT-PMP, deterministic OTel-driven balancing, real low-latency screen capture/input, `uv`/RGBA, editor/UI/Android behavior, or literal mTLS on intentional anonymous bootstrap routes. Case 05 is infrastructure-only and case 14 is fake-frame smoke.
* Route E2E diagnostics through `tests/e2e/lib/dump.sh`; artifacts must be sanitized before upload. Valid V1/V2 invite tokens are redacted only as complete token lines, JSON `token`/`secret` values are always redacted, and hashes/generic base64/embedded lookalikes remain intact.
* CI uses two full Go passes (coverage/ratchet and race) with the shell harness between them. Pull requests run E2E `pr` plus `network`, pushes run `full`, and the weekly schedule runs instrumented `full` with unit/E2E/union coverage.

---

## Skills Index

See [`.cursorrules.md`](../.cursorrules.md) skills table (`architecture-and-refactor-auditor`, `semantic-compression`, `continuous-granularity`, P2P/Android/testing/uischema/tdd).

---

## Roadmap (Context Only)

BadgerDB; Raft; gRPC real — do not implement unless asked. WebRTC DataChannel + screen fake frames exist (`BuildWebRTCHandler`, `BuildScreenHandler`).
