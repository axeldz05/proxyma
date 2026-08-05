---
name: integration-test-builder
description: Guía y patrones para la creación de tests de integración y concurrencia en Go para Proxyma. Define estrategias de mocks, eliminación de time.Sleep, inyección de fallos de disco/mTLS y verificación de goroutines sin fugas.
---

# Integration Test Builder - Proxyma Go

Esta skill define las mejores prácticas, patrones de mocks y plantillas de código para construir pruebas unitarias y de integración robustas, deterministas y rápidas en **Proxyma** Go.

---

## 1. Estrategia de Mocks (httptest vs. Interfaces Simuladas)

Proxyma combina comunicación de red HTTP/mTLS y RPCs P2P. La elección del tipo de mock depende de la capa bajo prueba:

| Escenario de Prueba | Herramienta Recomendada | Razón Técnica | Ejemplo de Referencia |
| :--- | :--- | :--- | :--- |
| **Pruebas de Handlers HTTP/mTLS y Enrutamiento** | `httptest.NewServer` / `httptest.NewUnstartedServer` | Evalúa la serialización JSON, middleware `mTLSGuard`, headers HTTP y la capa TLS real sin tocar la red pública. | [TestServer en server/fixtures_test.go](file:///home/drusila/Projects/proxyma/internal/server/fixtures_test.go) |
| **Pruebas de Orquestación Daemon (Server Core)** | `testutil.MockPeerClient` | Aísla la lógica interna del servidor evitando realizar llamadas HTTP secundarias hacia otros nodos simulados. Permite inyectar cierres/respuestas mediante callbacks. | [MockPeerClient en testutil/mocks.go](file:///home/drusila/Projects/proxyma/internal/testutil/mocks.go) |
| **Pruebas de Ejecución de Binarios / Compute** | `setupMockExecutable` (`go build` dinámico) | Genera un binario auxiliar temporal que lee JSON por STDIN y simula respuestas `success`, `crash` o `invalid_json`. | [setupMockExecutable en compute/helpers_test.go](file:///home/drusila/Projects/proxyma/internal/compute/helpers_test.go) |
| **Pruebas de Almacenamiento Físico / VFS** | `t.TempDir()` con Aislamiento | Garantiza un directorio limpio de BoltDB y CAS por cada sub-test, asegurando limpieza automática (`t.Cleanup`). | [storage_test.go](file:///home/drusila/Projects/proxyma/internal/storage/storage_test.go) |

---

## 2. Workflow de Creación de Tests (TDD Determinista sin `time.Sleep`)

### Reglas de Oro de QA en Proxyma:
1. **PROHIBIDO usar `time.Sleep`**: Las pausas fijas introducen parpadeos (*flaky tests*) y ralentizan la suite de pruebas. Usa canales de sincronización (`chan struct{}`), `select`, `context.WithTimeout` o `require.Eventually`.
2. **Aislamiento Total por Test**: Siempre utiliza `t.TempDir()` para las rutas de certificados, almacenamiento CAS y bases de datos BoltDB.
3. **Limpieza Garantizada con `t.Cleanup`**: Registra siempre la detención del servidor (`ts.Close()`), el cierre de canales y la cancelación de contextos en `t.Cleanup`.
4. **Verificación de Fugas de Goroutines**: Asegúrate de que las goroutines en segundo plano (ej. `downloadWorker`, `StartSweeper`) se detengan al cancelar el contexto.

### Workflow Paso a Paso (TDD):

```
 ┌──────────────────────────────────────────────────────────┐
 │ 1. Definir el Escenario (Caso Exitoso vs. Caso de Fallo) │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 2. Crear Fixture con `t.TempDir()` y `t.Cleanup()`        │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 3. Configurar Mocks o Servidor `httptest` con TLS        │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 4. Inyectar Cancelación de Contexto o Error Sintetizado   │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 5. Aseverar Resultado Empíricamente (Sin Timeouts Fijos)  │
 └────────────────────────────┴────────────────────────────┘
```

---

## 3. Plantillas Go Reutilizables

### A. Plantilla 1: Verificación de Cancelación de `context.Context` (Sin `time.Sleep`)

Mide el comportamiento inmediato cuando un contexto es cancelado durante un proceso asíncrono o descarga:

```go
func TestOperation_ContextCancellation(t *testing.T) {
    t.Parallel()
    
    // Crear un contexto que se cancela deliberadamente
    ctx, cancel := context.WithCancel(context.Background())
    
    // Canal para sincronizar el inicio del proceso
    started := make(chan struct{})
    errChan := make(chan error, 1)

    go func() {
        close(started) // Notificar que la goroutine arrancó
        err := myLongRunningOperation(ctx)
        errChan <- err
    }()

    // Esperar a que la goroutine inicie antes de cancelar
    <-started
    cancel() // Cancelación inmediata

    // Validar respuesta por canal con un timeout de seguridad alto
    select {
    case err := <-errChan:
        require.ErrorIs(t, err, context.ContextCanceled)
    case <-time.After(2 * time.Second):
        t.Fatal("La operación bloqueó la goroutine y no respetó la cancelación del contexto")
    }
}
```

### B. Plantilla 2: Test de Integración con `MockPeerClient` Inyectado

Prueba el orquestador sin depender de red real, inyectando comportamientos específicos por callback:

```go
func TestServer_DownloadBlob_MockedPeer(t *testing.T) {
    t.Parallel()

    mockClient := &testutil.MockPeerClient{
        OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
            require.Equal(t, "target-peer-addr", addr)
            require.Equal(t, "expected-hash-123", hash)
            // Devuelve un buffer en memoria simulando el blob
            return io.NopCloser(bytes.NewReader([]byte("fake-blob-content"))), nil
        },
    }

    tempDir := t.TempDir()
    cfg := protocol.NodeConfig{
        ID:          "test-node-1",
        StoragePath: tempDir,
        Workers:     2,
    }

    // Inicializar servidor de prueba usando la fixture
    ts := NewServer(t, cfg, mockClient)

    // Ejecutar llamada bajo prueba
    rc, err := ts.Server.DownloadBlobFromPeer(context.Background(), "target-peer-addr", "expected-hash-123")
    require.NoError(t, err)
    defer rc.Close()

    content, err := io.ReadAll(rc)
    require.NoError(t, err)
    require.Equal(t, "fake-blob-content", string(content))
}
```

### C. Plantilla 3: Inyección de Errores de Red y Handshake mTLS

Simula rechazo de certificados o fallos TLS en clientes HTTP:

```go
func TestP2P_TLSHandshakeFailure(t *testing.T) {
    t.Parallel()

    // Servidor httptest con certificado autofirmado no reconocido por el cliente
    badServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer badServer.Close()

    // Configurar cliente mTLS estricto que requiere CA oficial de Proxyma
    strictClient := badServer.Client()
    strictClient.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = false

    req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, badServer.URL, nil)
    require.NoError(t, err)

    _, err = strictClient.Do(req)
    require.Error(t, err, "El cliente debió rechazar la conexión por fallo de verificación mTLS/CA")
}
```

### D. Plantilla 4: Sincronización Determinista con `require.Eventually`

Sustituye cualquier `time.Sleep` al esperar estados asíncronos en memoria o bases de datos:

```go
func TestVFS_AsyncReplicationSync(t *testing.T) {
    t.Parallel()

    // Disparar sincronización en segundo plano...
    go engine.TriggerBackgroundSync()

    // Verificar determinísticamente hasta que la condición se cumpla o expire el timeout
    require.Eventually(t, func() bool {
        entry, err := engine.GetVFSFile("sync_file.txt")
        return err == nil && entry.HasLocal && !entry.Deleted
    }, 3*time.Second, 50*time.Millisecond, "La replicación del archivo VFS no se completó a tiempo")
}
```

---
