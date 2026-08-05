---
name: architecture-and-refactor-auditor
description: Guía de auditoría de arquitectura y refactorización para Proxyma Go. Define límites de responsabilidad entre paquetes (cmd, server, storage, p2p, compute, protocol, utils), detecta duplicación y guía el proceso de refactorización limpia.
---

# Architecture and Refactor Auditor - Proxyma Go

Esta skill establece las directrices de arquitectura de software, responsabilidades de paquetes y procedimientos de refactorización para el proyecto **Proxyma** en Go.

---

## Triggers (Cuándo usar esta Skill)

Debes consultar y aplicar esta skill en las siguientes situaciones:
1. **Nueva funcionalidad o endpoint**: Al incorporar un nuevo comando CLI, un nuevo servicio de compute, un handler HTTP en `server`, o una nueva llamada P2P.
2. **Modificación de API interna**: Al alterar la comunicación IPC por Unix Socket (`UnixRequest`/`UnixResponse`) o esquemas de mensajes inter-nodo (`protocol.go`).
3. **Detección de código duplicado**: Cuando identifiques bloques repetidos de configuración TLS, peticiones HTTP client/server, decodificación JSON/YAML o gestión de `context.Context`.
4. **Auditoría previa a PR / Commit**: Antes de solicitar review o realizar cambios estructurales profundos, para verificar la separación estricta de responsabilidades (SoC).

---

## Archivos a Inspeccionar Antes de Refactorizar

Antes de refactorizar o mover código, inspecciona los archivos principales de cada módulo para comprender el flujo de datos:

### 1. Capa CLI ([cmd/proxyma](file:///home/drusila/Projects/proxyma/cmd/proxyma))
* [main.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/main.go): Punto de entrada binario `proxyma`.
* [root.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/root.go): Comando raíz Cobra, flags globales e invocación de subcomandos.
* [run.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/run.go): Inicio del demonio ejecutor en segundo plano.
* [helpers.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/helpers.go): Utilidades compartidas de configuración CLI y conexión Unix Socket IPC.
* [invite.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/invite.go), [join.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/join.go), [vfs.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/vfs.go), [sync.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/sync.go), [service.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/service.go): Subcomandos específicos que envían órdenes al demonio.

### 2. Capa Servidor y Orquestación ([internal/server](file:///home/drusila/Projects/proxyma/internal/server))
* [server.go](file:///home/drusila/Projects/proxyma/internal/server/server.go): Ciclo de vida del servidor demonio, servidor HTTP mTLS.
* [peers.go](file:///home/drusila/Projects/proxyma/internal/server/peers.go): Topología (`AddPeer`/`AnnouncePresence`/`RemovePeer`).
* [unix_listener.go](file:///home/drusila/Projects/proxyma/internal/server/unix_listener.go): Dispatcher IPC unix → métodos `Local*` (`writeUnixResponse`, `announceAndSync`).
* [service_catalog.go](file:///home/drusila/Projects/proxyma/internal/server/service_catalog.go): **`LocalServiceDetail`**, Load/Add/Remove, **`applyServiceAction`**, **`NotifyService*`**, Discover.
* [service_exec.go](file:///home/drusila/Projects/proxyma/internal/server/service_exec.go): Run/Stream + ingest outputs.
* [vfs_sync.go](file:///home/drusila/Projects/proxyma/internal/server/vfs_sync.go): Sync, `fetchBlobFromPeer`, `LocalVFSUpload` / `LocalVFSSubscribe` / `LocalLogs`.
* [peer_rpc.go](file:///home/drusila/Projects/proxyma/internal/server/peer_rpc.go): **`callPeer` / `forEachPeer` / `mapEachPeer`** + timeouts `PeerRPC*`.
* [nat.go](file:///home/drusila/Projects/proxyma/internal/server/nat.go): NAT + **`advertisedTCPPort`**.
* [handlers.go](file:///home/drusila/Projects/proxyma/internal/server/handlers.go): Solo `MountHandlers` (wire-up).
* [mtls.go](file:///home/drusila/Projects/proxyma/internal/server/mtls.go), [peer_handlers.go](file:///home/drusila/Projects/proxyma/internal/server/peer_handlers.go), [cluster_handlers.go](file:///home/drusila/Projects/proxyma/internal/server/cluster_handlers.go), [stream_handlers.go](file:///home/drusila/Projects/proxyma/internal/server/stream_handlers.go): HTTP por dominio.
* [compute_bridge.go](file:///home/drusila/Projects/proxyma/internal/server/compute_bridge.go): Bidding (`mapEachPeer`) y despacho (`DispatchTask` owns register/fail remoto).
* [relay.go](file:///home/drusila/Projects/proxyma/internal/server/relay.go), [bandwidth.go](file:///home/drusila/Projects/proxyma/internal/server/bandwidth.go): Relay, telemetría.

### 3. Capa P2P y Seguridad ([internal/p2p](file:///home/drusila/Projects/proxyma/internal/p2p))
* [tls.go](file:///home/drusila/Projects/proxyma/internal/p2p/tls.go): CA/CSR/`LoadNodeTLS`/`newNodeCertTemplate`/`CAKeyPath`/`CACertPaths`.
* [p2p_client.go](file:///home/drusila/Projects/proxyma/internal/p2p/p2p_client.go): Cliente RPC (`DefaultRPCTimeout`).
* [helpers.go](file:///home/drusila/Projects/proxyma/internal/p2p/helpers.go): `FirstQUICAddr`, `ForwardRelay`, `VerifyPeerCN`.
* [holepunch.go](file:///home/drusila/Projects/proxyma/internal/p2p/holepunch.go): `HolePunchPingPayload` / `ParseHolePunchPing`.
* [router.go](file:///home/drusila/Projects/proxyma/internal/p2p/router.go), [join.go](file:///home/drusila/Projects/proxyma/internal/p2p/join.go).

### 4. Capa de Almacenamiento y VFS ([internal/storage](file:///home/drusila/Projects/proxyma/internal/storage))
* [storage_engine.go](file:///home/drusila/Projects/proxyma/internal/storage/storage_engine.go): Motor de orquestación y `StageLocalFile`.
* [vfs.go](file:///home/drusila/Projects/proxyma/internal/storage/vfs.go): BoltDB metadatos vía `boltGetJSON`/`boltPutJSON`/`boltLoadMapJSON`.
* [bolt_json.go](file:///home/drusila/Projects/proxyma/internal/storage/bolt_json.go): Helpers JSON Bolt.
* [physical/storage.go](file:///home/drusila/Projects/proxyma/internal/storage/physical/storage.go): CAS local.

### 5. Capa de Cómputo ([internal/compute](file:///home/drusila/Projects/proxyma/internal/compute))
* [services_config.go](file:///home/drusila/Projects/proxyma/internal/compute/services_config.go): SSOT `services.json` + `BuildLocalServiceFromArgs` (escribe `InferUIHint`).
* [compute.go](file:///home/drusila/Projects/proxyma/internal/compute/compute.go), [handlerBuilder.go](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go), [registry.go](file:///home/drusila/Projects/proxyma/internal/compute/registry.go).

### 6. Protocolo y Utilidades
* [protocol.go](file:///home/drusila/Projects/proxyma/internal/protocol/protocol.go): Tipos + **`InferUIHint` / `EffectiveUIHint`** + **`VFSURI` / `ParseVFSURI`**.
* [http_utils.go](file:///home/drusila/Projects/proxyma/internal/utils/http_utils.go): `RespondJSON` / `DecodeJSONOrError` / `GetRequiredQueryParam`.
* [net_utils.go](file:///home/drusila/Projects/proxyma/internal/utils/net_utils.go): **`IsLoopbackHost`**, IPs, puertos.

---

## Reglas de Arquitectura (Límites de Responsabilidad)

Cada paquete Go en Proxyma tiene responsabilidades estrictas para preservar una arquitectura limpia y desacoplada:

### 1. `cmd/proxyma` (CLI Layer)
* **DEBE**:
  * Parsear argumentos de línea de comandos y banderas mediante Cobra.
  * Formatear y enviar peticiones `UnixRequest` al demonio a través del socket Unix (`/tmp/proxyma.sock`).
  * Imprimir respuestas amigables al usuario en formato texto o JSON.
* **NO DEBE**:
  * Operar directamente sobre la base de datos BoltDB (`internal/storage/vfs.go`).
  * Iniciar sockets mTLS o llamadas P2P directas a nodos remotos omitiendo el demonio.
  * Contener lógica de negocio compleja ni orquestación de tareas.

### 2. `internal/server` (Daemon Orchestration Layer)
* **DEBE**:
  * Administrar el ciclo de vida del servidor HTTP mTLS y del listener de socket Unix local.
  * Coordinar los módulos `p2p`, `storage` y `compute`.
  * Proteger los endpoints de red mediante `mTLSGuard` y validar la identidad de los certificados de los nodos.
  * Manejar la API de control local IPC (`local_api.go`).
* **NO DEBE**:
  * Construir ni manipular certs X.509 de bajo nivel (debe usar `internal/p2p`).
  * Manipular blobs físicos CAS directamente en disco sin pasar por `internal/storage`.
  * Ejecutar directamente procesos del sistema o scripts sin pasar por `internal/compute`.

### 3. `internal/storage` (VFS & CAS Storage Engine)
* **DEBE**:
  * Indexar metadatos de archivos (haches SHA-256, rutas VFS) en BoltDB.
  * Leer y escribir blobs inmutables CAS en el almacenamiento físico local (`physical/storage.go`).
  * Gestionar la cola de descargas/replicación de blobs faltantes.
* **NO DEBE**:
  * Iniciar llamadas HTTP/RPC de red a otros nodos directamente.
  * Manejar certificados mTLS ni configuraciones P2P.
  * Parsear banderas de CLI ni interactuar con sockets IPC Unix.

### 4. `internal/p2p` (P2P Mesh & Crypto Layer)
* **DEBE**:
  * Generar claves privadas, solicitudes de firma de certificados (CSR) y certificados de nodo/CA.
  * Gestionar el ruteo de la topología P2P y las conexiones HTTP/QUIC entre nodos.
  * Ejecutar el flujo de invitación, tokens de enrolamiento y hole punching UDP.
* **NO DEBE**:
  * Persistir estado de base de datos VFS fuera de sus propias credenciales/certificados crypto.
  * Contener lógica de orquestación de pipelines de cómputo.
  * Depender de paquetes de nivel superior como `internal/server`.

### 5. `internal/compute` (Compute Engine)
* **DEBE**:
  * Ejecutar tareas asíncronas respetando los límites del pool de semáforos de concurrencia.
  * Construir handlers para ejecuciones tipo script/binary y stubs gRPC.
  * Garantizar cancelación segura vía `context.Context` con timeouts.
* **NO DEBE**:
  * Administrar certs mTLS ni la topología de la red P2P.
  * Operar directamente sobre la base de datos de metadatos VFS.

### 6. `internal/protocol` (Shared Contracts)
* **DEBE**:
  * Contener únicamente estructuras Go puras, constantes de protocolo y definiciones de tipos sin efectos secundarios.
  * Mantenerse en la raíz del árbol de dependencias internas (0 dependencias de otros paquetes `internal/*`).
* **NO DEBE**:
  * Importar ningún paquete dentro de `internal/*` (`internal/server`, `internal/p2p`, `internal/storage`, etc.).
  * Ejecutar operaciones de I/O de red o disco.

### 7. `internal/utils` (Stateless Helpers)
* **DEBE**:
  * Proveer utilidades estáticas y funciones puras para manipulación HTTP, parseo de IPs, STUN y métricas.
* **NO DEBE**:
  * Retener estado global de la aplicación ni importar paquetes de dominio con estado.

---

## Checklist de Refactorización (Paso a Paso)

Para detectar y mover código duplicado a paquetes comunes sin romper contratos existentes:

```
 ┌─────────────────────────────────────────────────────────┐
 │ 1. Identificar Duplicación (Detección de patrones)      │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 2. Ubicar o Crear la Abstracción Común                  │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 3. Aplicar Guardas de Límite (Slice Boundary Guards)    │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 4. Mantener la Granularidad Continua (3 Niveles)        │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌─────────────────────────────────────────────────────────┐
 │ 5. Verificación Empírica (Compilación y Tests)          │
 └────────────────────────────┴────────────────────────────┘
```

### Paso 1: Identificación de Patrones Duplicados

Al revisar el código, busca los siguientes patrones recurrentes:
* **Fan-out de peers**: No reinventar loops + timeouts + liveness — usar `callPeer` / `forEachPeer` / `mapEachPeer` / `firstPeer`.
* **Pipeline persist**: Un solo camino — `applyPipelineAction` (Local* + gossip).
* **Service persist**: Un solo camino — `applyServiceAction` (espejo pipelines) + `NotifyService*`.
* **Schema fill**: `protocol.NormalizeServiceSchema`; actions `ActionAdd`/`ActionRemove`.
* **Param UI/defaults**: `DescribeParameter` / `CoerceDefault` / `ValidateValue` — no switches de tipo en CLI/bind.
* **Result path**: `ResultLocalPath` / `OutputHashFromOutputs` — Android no snifar keys inventadas.
* **Task register/fail remoto**: Solo `DispatchTask`.
* **UIHint / pickers**: `InferUIHint` / `EffectiveUIHint` (DTO bind siempre emite effective).
* **Schema detail**: `LocalServiceDetail` / `LookupServiceSchema` / `GetServiceSchema`.
* **Errores bind/CLI**: `BindErrorJSON` / `ParseBindError` / `IsBindError`; VFS open vía `ResolveLocalBlob`.
* **Respuestas JSON HTTP**: `utils.RespondJSON` / `DecodeJSONOrError` / `HTTPSuccess`.
* **TLS / cert**: `LoadNodeTLS`, `WriteNodePEMs`, `HashCertDER` / `CAHashFromPEM` / `TLSConfigTrustCAHash`, paths helpers, `PeerCNFromTLS` / `VerifyTLSPeerCN`.
* **HTTP client**: `p2p.NewHTTPClient` (streams: timeout 0 + ctx).
* **QUIC addr**: `FormatQUICAddr` / `ParseQUICAddr` / `FirstQUICAddr`.
* **Hole-punch**: `HolePunchPingPayload` / `ParseHolePunchPing` / `BurstPings`.
* **IPC Unix**: `dispatchUnix*`; VFS `LocalVFS*` / `ResolveLocalBlob`.
* **Bolt**: `boltGetJSON` / `boltPutJSON` / `boltLoadMapJSON` / `boltPutFlag` / `boltHasKey`.
* **CAS upsert**: `UpsertAndSubscribe` / `deleteBlobIfOrphan`.
* **VFS URI**: `protocol.VFSURI` / `ParseVFSURI` / `IsVFSURI`.
* **Puerto TCP**: `protocol.DefaultTCPPort` + `configTCPPort` / `advertisedTCPPort`.
* **NDJSON**: `utils.WriteNDJSON` / `PumpJSONEncode` / `PumpJSONDecode` / `ForEachNDJSON`.
* **Net utils**: `StripURLScheme` / `ClientHost` / `FileExists` / `GetRoutableLocalIPs`.
* **Boilerplate CLI**: PersistentFlag `cliStorage` only; no dial propio.

### Paso 2: Ubicación de Abstracciones

* Si la utilidad es una función pura estática (ej. formateo HTTP, utilidades de red) -> mover a `internal/utils`.
* Si es una estructura de datos o contrato común -> mover a `internal/protocol`.
* Si es lógica crypto/mTLS -> mover a `internal/p2p/tls.go`.

### Paso 3: Aplicación de Guardas de Límite (Slice Boundary Guards)

Al recortar, iterar o modificar slices (ej. borrado de claves, rotación de colecciones), añade siempre guardas explícitas de límites según las reglas del proyecto:

```go
// Ejemplo de guarda de límite obligatoria en Proxyma:
if toDelete > len(keysToDelete) {
    toDelete = len(keysToDelete)
}
keysToDelete = keysToDelete[:toDelete]
```

### Paso 4: Granularidad Continua en 3 Niveles

Asegúrate de que los helpers de nivel superior no eliminen el acceso a las primitivas de nivel inferior:

1. **Nivel 1 (Primitiva de Bajo Nivel)**: Funciones/métodos directos de configuración (ej. `tls.Config` crudo con callbacks).
2. **Nivel 2 (Wrapper Comprimido)**: Helper con valores por defecto seguros (ej. `LoadNodeTLS(...)`).
3. **Nivel 3 (Utility de Alto Nivel)**: Invocación directa desde el orquestador (ej. `server.SetTLSConfigs(...)`).

### Paso 5: Verificación Empírica

1. Ejecuta siempre la suite de pruebas del paquete afectado:
   ```bash
   go test -v ./internal/server/...
   go test -v ./internal/p2p/...
   go test -v ./cmd/proxyma/...
   ```
2. Revisa que no existan ciclos de importación (`import cycle not allowed`).

---
