# Changelog - Proxyma P2P Dynamic Clustering Update

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
