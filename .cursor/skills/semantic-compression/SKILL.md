---
name: semantic-compression
description: Write maintainable code by applying semantic compression. Avoid premature abstraction; extract reusable parts only after two or more identical use cases appear. Start by making code usable, then refactor bottom-up to remove duplication. Includes Proxyma SSOT helpers already compressed.
---

# Semantic Compression

## Core Philosophy
Treat code like a dictionary compressor that runs continuously. Write concrete working code first. Compress only when duplication emerges. Abstractions arise from real examples, not upfront design.

## Rules

1. **Write specific code first** — ignore “reusability” until needed.
2. **Wait for the second occurrence** — extract when you see it again (≥2; Proxyma golden rule often uses **>2 zones** as the trigger to force compression).
3. **Compress by pulling out shared parts**:
   - Group repeated locals into a struct (*shared stack frame*).
   - Wrap repeated blocks into a plain function.
   - Turn functions into methods on the shared struct.
4. **Postpone fragile pre-calculations**.
5. **Keep the call site readable** — recipe-style, minimal noise.

## Proxyma Golden Rule

> If changing one implementation / adding one variant requires editing **more than 2 code zones**, there is repeated behavior — compress.

After compression, altering that behavior should touch **one** SSOT (plus thin call sites).

## Already Compressed in Proxyma (Do Not Duplicate)

| Pattern | SSOT location |
|---------|----------------|
| `services.json` load/save/build/upsert/delete | `internal/compute/services_config.go` |
| Handler from `ServiceType` + exec | `compute.BuildHandler` |
| Peer fan-out + SetPeerOnline/Offline | `internal/server/peer_rpc.go` (`callPeer`, `forEachPeer`) |
| Named peer RPC timeouts | `PeerRPCShort`, `PeerRPCDefault`, `PeerRPCBlob`, … (`PeerRPCQUICWait` = `protocol.HolePunchWait`) |
| Dial / hole-punch / handler timeouts | `protocol.DialTimeout*` / `HolePunch*` / `HandlerDial*` |
| `/relay/forward` marshal/POST/decode | `p2p.ForwardRelay` |
| Download blob + store | `server.fetchBlobFromPeer` |
| Local path → CAS + VFS upsert | `storage.StageLocalFile` |
| Unix socket dial/write/read/NDJSON | `cmd/proxyma-bind`: `DialUnix`, `WriteUnixRequest`, `ReadUnixResponse`, `ScanUnixNDJSON` |
| Unary unix-or-local dispatch | `dispatchUnixOrLocal` / **`InvokeDomainAction`** (CallUnixUnary) |
| Admin action names + unix IPC strings | `shared/uischema.Registry` (`UnixAction`, `FindAction`, `MustUnixAction`) |
| Admin arg validate / payload JSON / projection | `ValidateActionArgs` / `NormalizePayloadJSON` / `ProjectRows` / `FormatBytes` |
| Daemon unix dispatch table | `internal/server/unix_handlers.go` (`unixHandlers` / `CallUnixUnary`) |
| CLI action dispatch | `executeActionLocal` → Normalize→Validate → `InvokeDomainAction` + `cliEscapes` |
| Unix socket path | `protocol.SockFileName` / `protocol.UnixSockPath` |
| Pipeline validate / cycle | `protocol.ValidatePipelineSchema` / `PipelineHasCycle` (`pipeline_validate.go`) |
| Bolt bucket names | `internal/storage/buckets.go` (`allBuckets`) |
| Admin param DTO | `uischema.ParameterDetail` (bind aliases; do not clone) |
| Raw service schema vs Android DTO | `GetServiceSchema` vs `GetServiceDetails` |
| Streaming type aliases | `ServiceType.Normalize()` / `IsStreaming()` |
| File/image UI | `ServiceParameter.UIHint` — consumers do not re-sniff names |
| Bandwidth JSON | `protocol.BandwidthStats` only |
| TLS construction | `p2p.LoadNodeTLS` |
| HTTP stream handlers | `BuildHTTP*` / `BuildHandler` (`server_stream`, `http_bidi`, …) |
| WebRTC DataChannel JSON | `compute.BuildWebRTCHandler` + `ServiceTypeWebRTC` |
| HTTP JSON helpers | `utils.RespondJSON` / `DecodeJSONOrError` |

## What to Avoid
* Class hierarchies from domain nouns before code exists.
* Deep inheritance / patterns before duplication.
* Abstraction from a **single** use case.
* Copying a “temporary” offline path that duplicates server logic (bind offline must call the same L2 as server).

## Quality Indicators
* High semantic density; changes are local; new variants follow one pattern; one point of truth for debugging.

## After You Compress
Update `.cursorrules.md`, `.agents/AGENTS.md`, and `architecture-and-refactor-auditor` / this skill if you added a new SSOT helper.

**Mantra:** First make it work, then make it reusable — but only after you’ve seen the same pattern twice (or N>2 zones).
