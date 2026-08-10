---
name: continuous-granularity
description: Build APIs with compression-oriented continuous granularity—high-level wrappers never eliminate lower-level control. Includes Proxyma L1/L2/L3 examples for unix IPC, services, peers, and VFS.
---

# Continuous Granularity

**Objective:** Maximize compaction while keeping **continuous granularity**—high-level wrappers never obscure lower-level control (no API holes).

## Core Principles

1. **Compression over a priori design** — write concrete code first; extract when patterns repeat.
2. **Total cost focus** — prefer simple local solutions over fragile meta-frameworks.
3. **Continuous granularity** — when bundling into a high-level function, **keep lower pieces public** and callable.

## Three-Tier Rule

```
Level 3: High-level utility     (e.g. dispatchUnixOrLocal, LocalServiceAdd)
Level 2: Compressed wrapper     (e.g. sendUnixSocketCommand, UpsertLocalService, callPeer)
Level 1: Low-level primitives   (e.g. DialUnix, WriteUnixRequest, SavePhysicalBlob, peerClient.Notify)
```

### Layering vs Destructive Modification
* **DO NOT** force a mid-level API to only support a new special case (breaks callers that need the mid level).
* **DO** leave L2 intact and add a thin L3 on top.

## Proxyma L1 / L2 / L3 Map

### Unix IPC (`cmd/proxyma-bind`)
| Tier | API |
|------|-----|
| L1 | `DialUnix`, `WriteUnixRequest`, `ReadUnixResponse`, `ScanUnixNDJSON` |
| L2 | `sendUnixSocketCommand`; `server.CallUnixUnary` |
| L3 | **`InvokeDomainAction`** / `NormalizeActionArgs` / `uischema.NormalizePayloadJSON`; `dispatchUnixOrLocal`; stream via `StreamService` |

### Admin UI actions (`shared/uischema`)
| Tier | API |
|------|-----|
| L1 | `ActionDetail` / `FindAction` / `UnixActionFor` / `ApplyDefaults` / `MissingRequired` / `SuccessMessage` / `NormalizePayloadJSON` |
| L2 | Daemon `unixHandlers` + `CallUnixUnary` |
| L3 | CLI `executeActionLocal` → Invoke + `cliEscapes`; Cobra from `VisibleRegistry("cli")` |

### Services
| Tier | API |
|------|-----|
| L1 | `LoadServicesMap` / `SaveServicesMap` |
| L2 | `BuildLocalServiceFromArgs`, `UpsertLocalService`, `BuildHandler` |
| L3 | `LocalServiceAdd` / bind `AddService` |

### Peers
| Tier | API |
|------|-----|
| L1 | `peerClient.*`, `SetPeerOnline` / `SetPeerOffline` |
| L2 | `callPeer` |
| L3 | `forEachPeer` |

### VFS blobs
| Tier | API |
|------|-----|
| L1 | `DownloadBlob`, `SavePhysicalBlob`, `Upsert`, `StoreRemoteBlob` |
| L2 | `fetchBlobFromPeer`, `StageLocalFile` |
| L3 | downloadWorker / DispatchTask / VFS stager callbacks |

### Relay
| Tier | API |
|------|-----|
| L1 | HTTP RoundTrip to `/relay/forward` |
| L2 | `ForwardRelay` |
| L3 | `sendRelayMessage` / RoundTrip Phase 2 rebuild `http.Response` / join fallback |

### Service metadata
| Tier | API |
|------|-----|
| L1/L2 | `GetServiceSchema` → `protocol.ServiceSchema` |
| L3 | `GetServiceDetails` → Android `ServiceDetail` + `uiHint` |

## Checklist Before Adding a Helper
* Pattern repeated ≥2–3 times?
* Caller can drop to L1 if the new helper does not fit?
* Prefer an extra explicit param over opaque framework state?

## When Refactoring
1. Isolate repeated chunks.  
2. Package as L2; keep L1 exported.  
3. Add L3 only for convenience.  
4. Verify raw L1 calls still weave between L2/L3.

## After Changing Tiers
Update `.cursorrules.md`, `.agents/AGENTS.md`, and this skill’s Proxyma map.
