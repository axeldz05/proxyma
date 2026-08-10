---
name: architecture-and-refactor-auditor
description: Guía de auditoría de arquitectura y refactorización para Proxyma Go. Define límites de responsabilidad entre paquetes (cmd, server, storage, p2p, compute, protocol, utils), detecta duplicación y guía el proceso de refactorización limpia. Incluye el mapa SSOT post-compresión semántica.
---

# Architecture and Refactor Auditor - Proxyma Go

Directrices de arquitectura, límites de paquetes y mapa de helpers SSOT actuales.

**Al terminar una implementación que mueva archivos/APIs:** actualizar esta skill + `.cursorrules.md` + `.agents/AGENTS.md`.

---

## Triggers

1. Nueva funcionalidad o endpoint (CLI, compute, HTTP server, P2P).
2. Cambio de contratos IPC (`UnixRequest`/`UnixResponse`) o `protocol.go`.
3. Detección de duplicación (regla de oro: **>2 zonas** para un mismo comportamiento).
4. Auditoría previa a PR / refactor estructural.

---

## Mapa de Archivos Actual (Inspeccionar Primero)

### 1. CLI — `cmd/proxyma`
* `root.go` — Cobra from `uischema.VisibleRegistry("cli")`; help via `NormalizeActionArgs`.
* `cli_actions.go` — `executeActionLocal` → Normalize→Validate → `cliEscapes` OR **`InvokeDomainActionPrepared`**.
* `service_help.go` — help/schema; `ParseInputsToJSON` → `NormalizePayloadJSON`; `sampleValue`.
* `helpers.go`, `run.go`, `init.go`.

### 2. Bind — `cmd/proxyma-bind`
* `bind.go` / `invoke.go` — L1 unix + L3 **`InvokeDomainAction`** / L2 **`InvokeDomainActionPrepared`** / `NormalizeActionArgs` / **`offlineHooks`**.
* `service.go` — wrappers thin sobre Invoke; `ParameterDetail` = `uischema.ParameterDetail`.
* `storage.go`, `peers.go`, `telemetry.go`, `cluster.go` — `InvokeDomainAction` one-liners.

### 3. Server — `internal/server`
* `server.go` — ciclo de vida demonio / mTLS HTTP.
* `unix_handlers.go` — **`unixHandlers`** map (SSOT dispatch); `unix_listener.go` — accept + lookup.
* `local_services.go` — Load/Run/Stream/Add/Remove servicios.
* `local_pipelines.go` — pipelines + `NotifySchema`; validate via **`protocol.ValidatePipelineSchema`**.
* `local_api.go` — invite, bandwidth, peers list.
* `peer_rpc.go` — **`callPeer` / `forEachPeer`** + `PeerRPC*` (`PeerRPCQUICWait` = `HolePunchWait`).
* `vfs_sync.go` — sync, `downloadWorker`, **`fetchBlobFromPeer`**.
* `compute_bridge.go` — bids, `DispatchTask`, QUIC ensure.
* `handlers.go` — mTLS HTTP; invite HTTP → `LocalInviteGenerate`.
* `relay.go`, `nat.go`, `bandwidth.go`, `tls_rotation.go`, `registry.go`, `invite.go`.

### 4. P2P — `internal/p2p`
* `p2p_client.go` — `PeerClient` **incluye** routing: `UpdatePeerRoute`, `SetNodeID`, `SetOwnAddress`, `CloseIdleConnections`, `SetQUICManager` (sin type assertions).
* `helpers.go` — `doJSON`/`sendRequest`, **`ForwardRelay`**, `FirstQUICAddr`, `VerifyPeerCN`.
* `tls.go` — CA/CSR/`LoadNodeTLS`.
* `router.go`, `holepunch.go`, `join.go` — dial/punch timeouts from `protocol.DialTimeout*` / `HolePunch*`.

### 5. Storage — `internal/storage`
* `buckets.go` — Bolt bucket name consts + `allBuckets`.
* `storage_engine.go` — **`StageLocalFile`** (L2); L1 `SavePhysicalBlob` + `Upsert` públicos.
* `vfs.go`, `physical/storage.go`.

### 6. Compute — `internal/compute`
* **`services_config.go`** — SSOT `services.json`: Load/Save/Build/Upsert/Delete + **`BuildHandler`** (`HandlerDialUnary`/`HandlerDialStream`).
* `handlerBuilder.go`, `compute.go`, `registry.go`.

### 7. Protocol / Utils / UISchema
* `protocol.go` — `LocalService`, `ServiceType.Normalize`/`IsStreaming`, `ServiceParameter.UIHint`, `BandwidthStats`, `VFSFileStatus`.
* `pipeline_validate.go` — **`ValidatePipelineSchema` / `PipelineHasCycle`**.
* `timeouts.go` — `RPCTimeout*` / `DialTimeout*` / `HolePunch*` / `HandlerDial*`.
* `uischema` — Registry, `ValidateActionArgs`, **`NormalizePayloadJSON`**, `ProjectRows` / `FormatBytes`.
* `utils/http_utils.go` — `RespondJSON` / `DecodeJSONOrError`.

---

## Regla de Oro (Duplicación)

Si al cambiar una implementación o agregar una variante hay que tocar **más de 2 zonas**, hay comportamiento repetido → comprimir.

**Ya comprimido (NO reinventar):**

| Comportamiento | SSOT |
|----------------|------|
| services.json CRUD/parse | `compute.services_config.go` |
| Fan-out peers + liveness | `server.peer_rpc.go` |
| Relay `/relay/forward` | `p2p.ForwardRelay` |
| Fetch blob remoto | `server.fetchBlobFromPeer` |
| Stage path local → VFS | `storage.StageLocalFile` |
| Unix dial/read/write/stream | bind Dial/Write/Read/Scan |
| Admin domain.action + unix names | `shared/uischema.Registry` + `UnixAction` |
| Admin arg validation / projection | `uischema.ValidateActionArgs` / `NormalizePayloadJSON` / `ProjectRows` / `FormatBytes` / `BandwidthStatsRows` |
| Daemon unix dispatch | `server.unix_handlers.go` |
| CLI action dispatch | `cmd/proxyma` `executeActionLocal` (Normalize→Validate) → Prepared / `cliEscapes` |
| Bind offline fallbacks | `offlineHooks` in `invoke.go` (compute L2) |
| Socket path | `protocol.UnixSockPath` |
| Invite TTL | `protocol.DefaultInviteMinutes` |
| Dial / punch / handler timeouts | `protocol.DialTimeout*` / `HolePunch*` / `HandlerDial*` |
| Schema streaming CLI | `GetServiceSchema` |
| TLS config | `p2p.LoadNodeTLS` |

---

## Límites de Responsabilidad (SoC)

### `cmd/proxyma`
* DEBE: Cobra, IPC vía bind/unix, formateo UX.
* NO DEBE: BoltDB, mTLS/P2P directo, lógica de negocio pesada.

### `cmd/proxyma-bind`
* DEBE: puente in-process o unix hacia demonio; JSON strings para gomobile.
* NO DEBE: clonar RMW de `services.json` a mano; sniffing de tipos de dominio.

### `internal/server`
* DEBE: orquestar p2p/storage/compute; IPC local; mTLSGuard.
* NO DEBE: crypto X.509 low-level; CAS directo; exec sin compute.

### `internal/storage`
* DEBE: VFS/Bolt + CAS.
* NO DEBE: HTTP a peers; certs; CLI.

### `internal/p2p`
* DEBE: TLS, routing, join/invite crypto, holepunch, relay client helpers.
* NO DEBE: VFS DB; pipelines; depender de `server`.

### `internal/compute`
* DEBE: handlers, registry, services.json helpers, pool de tareas.
* NO DEBE: mTLS topology; VFS metadata DB.

### `internal/protocol`
* DEBE: tipos puros, 0 imports `internal/*` de dominio.
* NO DEBE: I/O de red/disco (salvo Load/Save config existentes).

---

## Checklist de Refactor

1. Identificar duplicación (N>2).
2. Ubicar SSOT existente (tabla arriba) o crear L1→L2→L3 sin tapar L1.
3. Slice boundary guards.
4. Granularidad continua: L3 reemplazable por composición de L1/L2.
5. `go test` paquetes tocados; sin ciclos de import.
6. **Actualizar** `.cursorrules.md`, `AGENTS.md` y esta skill si cambió el mapa.

### Anti-patrones residuales a vigilar
* Type assertion sobre `peerClient` → ampliar interfaz.
* Dial unix duplicado fuera de bind L1.
* Tags JSON distintos para el mismo DTO.
* Heurísticas file/image en Kotlin cuando ya viene `uiHint`.

---
