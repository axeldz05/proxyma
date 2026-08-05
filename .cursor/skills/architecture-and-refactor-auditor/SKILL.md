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
* [server.go](file:///home/drusila/Projects/proxyma/internal/server/server.go): Ciclo de vida del servidor demonio, servidor HTTP mTLS y listener de Unix domain socket.
* [local_api.go](file:///home/drusila/Projects/proxyma/internal/server/local_api.go): API de control local IPC (`handleUnixConnection`, validación de pipelines y servicios locales).
* [handlers.go](file:///home/drusila/Projects/proxyma/internal/server/handlers.go): Endpoints HTTP inter-nodo y guardia de autenticación mTLS ([mTLSGuard](file:///home/drusila/Projects/proxyma/internal/server/handlers.go#L19)).
* [vfs_sync.go](file:///home/drusila/Projects/proxyma/internal/server/vfs_sync.go): Sincronización VFS en segundo plano y workers de descarga de blobs.
* [compute_bridge.go](file:///home/drusila/Projects/proxyma/internal/server/compute_bridge.go): Búsqueda de licitaciones (bidding) de servicios y despacho de tareas inter-nodo.
* [nat.go](file:///home/drusila/Projects/proxyma/internal/server/nat.go), [relay.go](file:///home/drusila/Projects/proxyma/internal/server/relay.go), [bandwidth.go](file:///home/drusila/Projects/proxyma/internal/server/bandwidth.go): Mapeo NAT, cliente de polling de relay y telemetría de ancho de banda.

### 3. Capa P2P y Seguridad ([internal/p2p](file:///home/drusila/Projects/proxyma/internal/p2p))
* [tls.go](file:///home/drusila/Projects/proxyma/internal/p2p/tls.go): Generación y rotación de CA, firmado de CSRs X.509 y carga de credenciales TLS.
* [p2p_client.go](file:///home/drusila/Projects/proxyma/internal/p2p/p2p_client.go): Cliente RPC para comunicación con nodos remotos (`DownloadBlob`, `SubmitTask`, etc.).
* [router.go](file:///home/drusila/Projects/proxyma/internal/p2p/router.go), [join.go](file:///home/drusila/Projects/proxyma/internal/p2p/join.go), [invite.go](file:///home/drusila/Projects/proxyma/internal/p2p/invite.go): Tabla de ruteo P2P, enrolamiento de nodos e invitaciones por token.
* [holepunch.go](file:///home/drusila/Projects/proxyma/internal/p2p/holepunch.go): Perforación de puertos UDP y sesiones QUIC.

### 4. Capa de Almacenamiento y VFS ([internal/storage](file:///home/drusila/Projects/proxyma/internal/storage))
* [storage_engine.go](file:///home/drusila/Projects/proxyma/internal/storage/storage_engine.go): Motor de orquestación de almacenamiento y cola de replicación.
* [vfs.go](file:///home/drusila/Projects/proxyma/internal/storage/vfs.go): Base de datos BoltDB para índices y metadatos del VFS.
* [physical/storage.go](file:///home/drusila/Projects/proxyma/internal/storage/physical/storage.go): Almacenamiento direccionable por contenido (CAS) en el sistema de archivos local.

### 5. Capa de Cómputo ([internal/compute](file:///home/drusila/Projects/proxyma/internal/compute))
* [compute.go](file:///home/drusila/Projects/proxyma/internal/compute/compute.go): Motor ejecutor de tareas asíncronas con control de concurrencia (pool de semáforos).
* [handlerBuilder.go](file:///home/drusila/Projects/proxyma/internal/compute/handlerBuilder.go): Construcción de ejecutores para scripts locales y microservicios gRPC.
* [registry.go](file:///home/drusila/Projects/proxyma/internal/compute/registry.go): Registro local de tareas y manejadores de servicios.

### 6. Protocolo y Utilidades ([internal/protocol](file:///home/drusila/Projects/proxyma/internal/protocol), [internal/utils](file:///home/drusila/Projects/proxyma/internal/utils))
* [protocol.go](file:///home/drusila/Projects/proxyma/internal/protocol/protocol.go): Estructuras de datos puras, esquemas JSON y constructor del logger global.
* [http_utils.go](file:///home/drusila/Projects/proxyma/internal/utils/http_utils.go): Helpers HTTP (`RespondJSON`, `RespondError`, `DecodeJSONOrError`).
* [net_utils.go](file:///home/drusila/Projects/proxyma/internal/utils/net_utils.go), [stun.go](file:///home/drusila/Projects/proxyma/internal/utils/stun.go): Red e IP helpers, cliente STUN.

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
* **Respuestas JSON en Handlers HTTP**:
  - *Mal*: Repetir `w.Header().Set("Content-Type", "application/json")` y `json.NewEncoder(w).Encode(...)` en múltiples endpoints HTTP.
  - *Solución*: Usar `utils.RespondJSON(w, status, payload)` y `utils.RespondError(w, status, message)` de [http_utils.go](file:///home/drusila/Projects/proxyma/internal/utils/http_utils.go).
* **Configuración de TLS Client/Server**:
  - *Mal*: Instanciar `&tls.Config{...}` disperso en `server.go`, `p2p_client.go`, `holepunch.go` y tests.
  - *Solución*: Centralizar los constructores en [tls.go](file:///home/drusila/Projects/proxyma/internal/p2p/tls.go) (`NewServerTLSConfig`, `NewClientTLSConfig`).
* **Boilerplate IPC de Socket Unix en CLI**:
  - *Mal*: Repetir conexión dial a `/tmp/proxyma.sock` y codificación `json.NewEncoder` en cada comando Cobra.
  - *Solución*: Utilizar la función centralizadora de IPC en [helpers.go](file:///home/drusila/Projects/proxyma/cmd/proxyma/helpers.go).

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
