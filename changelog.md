# Changelog - Proxyma P2P Dynamic Clustering Update

## 20-06-2026
### Refactorización del Servidor (Granularidad Continua)
* **Subcomponentes del Servidor**: Se dividió el struct monolítico `Server` en cuatro administradores independientes y seguros para subprocesos: `PeerRegistry` (contactos y servicios), `InviteManager` (tokens de emparejamiento), `BandwidthTracker` (telemetría de red) y `RelayManager` (colas y canales de relay). Se mantuvieron métodos delegadores en `Server` para preservar la compatibilidad con el resto del código y los tests.

### Compresión Semántica y Deduplicación (UI & Core)
* **CountingReadCloser**: Centralizado en `internal/utils` para eliminar la duplicidad en el rastreo de progreso de lectura.
* **Refactorización de Formularios en Fyne**: Extraído `buildParameterWidget` en `helpers.go` para simplificar la creación y validación de parámetros de servicios en la UI.
* **Telemetría de Velocidad unificada**: Creado `formatBandwidthSuffix` en `proxymaui.go` para formatear de forma uniforme las etiquetas con velocidad de red.
* **getRunningServer()**: Incorporado en `helpers.go` para encapsular de forma segura la adquisición y control de `srv` bajo el mutex de la UI de Fyne.
* **Cálculo de Ancho de Banda**: Simplificado en `bandwidth.go` abstrayendo los bucles de podado e históricos en `pruneHistory` y `sumCategory`.

### Limpieza de Código Muerto
* **Eliminación de funciones redundantes**: Se borraron funciones genéricas y métodos sin uso (`Map`, `FindFileAndDo`, `ExistsFileRelativeToBase`) bajo `internal/storage/physical` para reducir el costo de mantenimiento.

## 21-05-2026
### Relay y STUN
* **Relay Fallback**: Los nodos detrás de NAT realizan long-polling (`/relay/poll`) autenticados mediante mTLS. Si la conexión directa falla, el emisor redirige un `RelayRequest` con un `ReqID` seguro al Sponsor (`/relay/forward`). El receptor procesa el mensaje localmente y responde por `/relay/reply`.
* **STUN-like Detection**: Al anunciarse un nodo (`/peers/announce`), el servidor detecta su IP pública de origen (`r.RemoteAddr`), reconstruye la dirección percibida con el puerto del nodo y la propaga en el clúster para habilitar la conectividad directa.

## 20-05-2026
### Refactorizaciones (CLI & HTTP Handlers)
* **CLI helpers**: Se eliminó la duplicación de código en la lectura de configuración de los comandos (`init`, `join`, `run`, `sync`, `invite`, `service`) extrayéndolo a `cmd/helpers.go`.
* **Auto-generación de IDs**: Al inicializar (`proxyma init`) o unirse (`proxyma join`) a la red, el flag `--id` ahora es opcional. El sistema lo genera automáticamente usando el nombre del host.
* **Declaración de Servicios**: `proxyma service add` ahora permite pasar un archivo `.json` directamente, sin perder la funcionalidad original del CLI paramétrico.
* **Enrutamiento Go 1.22+**: Se adoptaron las nuevas capacidades de la librería estándar para especificar el método HTTP directamente en la ruta (`POST /ruta`), eliminando validaciones manuales redundantes en toda la capa de red (`internal/server`, `internal/compute`, `internal/storage`).
* **Compresión Semántica (JSON)**: Se reutilizó de manera centralizada `utils.DecodeJSON` para todas las lecturas de *payloads* POST.

### Mejoras de Arquitectura y Resoluciones
* **Workers Asíncronos**: El `ComputeEngine` ahora despacha las tareas entrantes de forma totalmente asíncrona creando nuevas *goroutines* bajo demanda, pero limitando la concurrencia a través de un semáforo según el número de *workers* configurados.
* **Sync Seguro via Unix Sockets**: Se eliminó por completo el endpoint público de sincronización HTTP (`/sync`). El comando `proxyma sync` ahora se comunica localmente mediante un Socket Unix (`proxyma.sock`) almacenado en el directorio de la aplicación, incrementando significativamente la seguridad y previniendo ataques de denegación de servicio remotos.
* **Validaciones Preventivas**: `HandleClusterJoin` ahora prueba alcanzar al nodo solicitante (TCP Dial Timeout) antes de otorgar un certificado. El endpoint de anuncios valida que no lleguen parámetros vacíos.
* **gRPC Stubs**: Se plantaron las bases de `BuildGRPCBidiHandler`, `BuildGRPCServerStreamHandler` y `BuildWebRTCHandler` en `handlerBuilder.go` para futuras implementaciones.


## 30-04-2026
### Funcionalidades
* **Pairing:** Se agregaron los comandos y endpoints `/peers/invite` y `/cluster/join`. Los nodos ahora pueden unirse a la red mediante un "Smart Token" de un solo uso que expira automáticamente (gestionado por una nueva Goroutine `inviteSweeper`).
* **Auto-descubrimiento:** Se implementó el endpoint `/peers/announce` y `/peers/add`. Cuando un nodo arranca, informa a su "Bootstrap Node", el cual propaga la identidad del nuevo nodo al resto de la red y le devuelve los peers del clúster.
* **Firmado Dinámico de Certificados:** Se añadieron las funciones `GenerateNodeCSR` y `SignCSR` en el módulo TLS. Los nodos nuevos ahora generan su propia llave privada y envían un CSR al clúster, el cual es firmado por la CA en tiempo real.
* **Middleware de mTLS:** Se creó el interceptor `mTLSGuard`. La configuración TLS base ahora es `VerifyClientCertIfGiven`, pero el middleware bloquea cualquier petición sin certificado válido, *excepto* la ruta de emparejamiento `/cluster/join`.
* **Persistencia de Configuración:** Se reemplazó el pase masivo de flags en la CLI por las funciones `SaveConfig` y `LoadConfig`, consolidando el estado del nodo en un archivo `config.json`.

### Refactorizaciones
* **Comando `run`:** Ya no requiere definir flags de red o rutas de certificados; lee directamente el `config.json` inicializado y arranca el servidor o anuncia su presencia si tiene un `BootstrapNode` definido.
* **Comando `sync`:** Se rediseñó para actuar como un cliente de control local. Ahora lee el `config.json` y envía una simple orden POST al demonio local (en segundo plano) para desencadenar la sincronización.
* **Ejecución de Sincronización P2P:** Ya no recibe una lista estática de IDs por parámetro. Ahora itera automáticamente de forma asíncrona sobre toda la libreta de contactos registrada en memoria (`srv.peers`).
* **Sincronización de Trabajadores:** Se inicializan explícitamente en la función constructora `server.New()`, garantizando que la cola de descargas P2P se procese desde el segundo cero.

### Eliminaciones
* **El Nodo Génesis (`cmd/certs.go`):** Se eliminó por completo el concepto de un nodo inicializador rígido. Los comandos antiguos `init` (global) e `issue` fueron borrados del código base.
* Se eliminó la dependencia de compartir la carpeta `/app/certs` entre contenedores Docker.

### Infraestructura y Testing
* **`docker-compose.yml`:** Se eliminaron los comandos de sobreescritura (ahora usan el `CMD` nativo del Dockerfile) y se removió el nodo génesis.
* **`e2e_test.sh`:** Ahora usa Contenedores Efímeros en la fase de aprovisionamiento, simulando el comportamiento real de administradores de red distribuidos.
* **Tests Unitarios:** Se actualizaron las firmas de las funciones y se añadió `TestUnauthorizedAccessIsRejectedAndPairingIsAllowed` para garantizar que el `mTLSGuard` bloquea intrusos pero permite a los nodos unirse al clúster.
* **`Dockerfile`:** Se añadió la variable de entorno base `PROXYMA_STORAGE=/app/data` y se configuró el flag `--debug` para que se muestren los logs Debug dados por Logger.
