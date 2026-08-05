---
name: e2e-cluster-testing
description: Guía de orquestación, infraestructura DevSecOps y depuración de pruebas E2E multinodo en Proxyma. Cubre Docker Compose, emparejamiento mTLS, validación VFS/SHA-256 y pipelines de CI/CD en GitHub Actions.
---

# E2E Cluster Testing & DevSecOps - Proxyma

Esta skill documenta la arquitectura de orquestación E2E, los scripts de prueba multinodo en Docker Compose, los protocolos de seguridad mTLS y las mejores prácticas de CI/CD para el proyecto **Proxyma**.

---

## 1. Protocolo de Ejecución E2E

Proxyma cuenta con dos entornos de orquestación E2E: desarrollo local dinámico y clúster multinodo aislado en contenedores Docker.

### A. Entorno de Desarrollo Local (`scripts/bootstrap_dev.sh`)

Utilizado para levantar un demonio Proxyma local con servicios preconfigurados (OCR, extracción de texto, notas en Obsidian, editor TUI) y archivos de muestra en VFS:

```bash
# Otorgar permisos y ejecutar el script de arranque local
chmod +x ./scripts/bootstrap_dev.sh
./scripts/bootstrap_dev.sh

# Interactuar con la instancia usando el wrapper generado 'pm'
~/.proxyma_dev/pm storage list
~/.proxyma_dev/pm service discover
```

### B. Ejecución de la Suite E2E Multinodo Completa (`tests/e2e/run.sh`)

Ejecuta todos los casos de prueba distribuidos en paralelo (`01_sync_ocr`, `02_network_partition`, `03_relay_fallback`, `04_node_churn`, `05_cgroups_limits`, `06_generic_file_ocr`, `07_udp_hole_punching`):

```bash
# Exportar UID/GID del host para evitar problemas de permisos en Docker
export HOST_UID=$(id -u)
export HOST_GID=$(id -g)

# Otorgar ejecución a los scripts
chmod +x ./tests/e2e/run.sh ./tests/e2e/cases/*.sh

# Correr la suite de pruebas E2E completa
./tests/e2e/run.sh
```

### C. Ejecución Individual de un Caso E2E

Para ejecutar un solo caso de prueba E2E de forma aislada durante el desarrollo:

```bash
export HOST_UID=$(id -u)
export HOST_GID=$(id -g)
./tests/e2e/cases/01_sync_ocr.sh
```

---

## 2. Flujo Completo de Prueba y Arquitectura de Clúster

Cada prueba E2E sigue un ciclo de vida strictly aislado mediante el helper [tests/e2e/lib/helpers.sh](file:///home/drusila/Projects/proxyma/tests/e2e/lib/helpers.sh):

```
 ┌──────────────────────────────────────────────────────────┐
 │ 1. Clean & Setup (`cleanup_e2e`, `E2E_PROJECT_NAME`)     │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 2. Bootstrapping de Nodos (`bootstrap_node`)             │
 │    - Genera CA local y certs mTLS en `/app/data/certs/` │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 3. Emparejamiento del Clúster (`join_cluster`)           │
 │    - Genera token de invitación (`cluster invite`)       │
 │    - Firma CSR y comparte topología mTLS (`cluster join`)│
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 4. Sincronización VFS e Integridad Criptográfica         │
 │    - Subida de archivos (`upload`)                       │
 │    - Propagación de metadatos VFS                        │
 │    - Verificación de suscripción previa (`subscribe`)    │
 │    - Comparación estricta de hash SHA-256 (`diff`)       │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 5. Teardown y Limpieza (`trap cleanup_e2e EXIT`)         │
 └────────────────────────────┴────────────────────────────┘
```

---

## 3. Guía de Depuración de Fallos Comunes

| Síntoma de Error | Causa Raíz Probable | Procedimiento de Resolución / Depuración |
| :--- | :--- | :--- |
| **`Permission denied` en carpetas `/app/data` dentro del contenedor** | El contenedor Docker se ejecutó como `root` o sin exportar `HOST_UID`/`HOST_GID`. | Verificar que `docker-compose.e2e.yml` contenga `user: "${HOST_UID}:${HOST_GID}"`. Asegurar `export HOST_UID=$(id -u) HOST_GID=$(id -g)` antes de invocar los scripts. |
| **`SSL certificate problem: unable to get local issuer certificate`** | `curl` no encuentra la CA o los certs mTLS del nodo no coinciden con la dirección. | Usar el helper `call_api node-X` que inyecta automáticamente `--cacert`, `--cert` y `--key` de `/app/data/certs/`. |
| **`Daemon socket did not appear`** | El proceso Proxyma falló al arrancar por colisión de puertos (ej. `8080` ocupado) o error de sintaxis en `config.json`. | Revisar los logs guardados en `tests/e2e/logs/<case_name>.log` o `~/.proxyma_dev/proxyma.log`. Ejecutar `pkill -9 -f "proxyma run"`. |
| **`File did not reach the VFS of node-X` (Timeout)** | La sincronización en segundo plano tardó más de lo esperado o la red Docker aislada no comunicó los nodos. | Verificar la conectividad mTLS ejecutando `call_api node-X GET <port> manifest` manualmente y revisar la regla `wait_for_condition`. |
| **Descarga prematura de blob sin suscripción** | Un nodo descargó el blob físico sin haberse suscrito explícitamente (`Subscribed: false`). | Violación del diseño de Proxyma. Verificar la lógica de `storage_engine.go` y la cola de descargas `downloadQueue`. |

---

## 4. Checklist DevSecOps para CI/CD (GitHub Actions)

Al crear o modificar flujos de trabajo en `.github/workflows/` ([ci.yml](file:///home/drusila/Projects/proxyma/.github/workflows/ci.yml) y [e2e.yml](file:///home/drusila/Projects/proxyma/.github/workflows/e2e.yml)), cumple estrictamente las siguientes reglas de seguridad y eficiencia:

### 1. Principio de Menor Privilegio (`permissions`)
* **Regla**: Declara siempre `permissions: contents: read` en el nivel superior del archivo YAML. No concedas permisos de escritura (`write`) a menos que sea estrictamente necesario (ej. publicación de releases).

### 2. Caché Eficiente de Módulos Go y Buildx
* Utiliza `actions/setup-go@v5` con `cache: true` habilitado.
* Configura `docker/setup-buildx-action@v3` para acelerar la construcción de capas Docker durante las pruebas E2E.

### 3. Preservación de Artefactos en Caso de Fallo
* Configura `actions/upload-artifact@v4` con la condición `if: failure()` para adjuntar los logs de `tests/e2e/logs/` con retención acotada (máximo 5 días):

```yaml
- name: Upload E2E Logs on Failure
  if: failure()
  uses: actions/upload-artifact@v4
  with:
    name: e2e-logs
    path: tests/e2e/logs/
    retention-days: 5
```

### 4. Permisos de Ejecución Explícitos
* Ejecutar siempre `chmod +x` sobre los scripts de pruebas antes de la invocación en los runners de GitHub Actions:
  ```yaml
  - name: Make scripts executable
    run: chmod +x ./tests/e2e/run.sh ./tests/e2e/cases/*.sh
  ```

---
