---
name: p2p-nat-relay-orchestrator
description: Guía de arquitectura P2P, perforación de NATs (UDP Hole Punching), descubrimiento STUN, mapeo UPnP y túneles de Relay por Long-Polling HTTP en Proxyma.
---

# P2P NAT & Relay Orchestrator - Proxyma

Esta skill proporciona las directrices técnicas, matriz de conectividad y procedimientos de diagnóstico para garantizar la comunicación inter-nodo en entornos de red heterogéneos y restrictivos (Detrás de NATs simétricos, CGNAT o firewalls corporativos).

---

## 1. Triggers (Cuándo usar esta Skill)

Consulta y aplica esta skill en los siguientes escenarios:
1. **Modificación de Módulos P2P o Red**: Al editar [internal/server/relay.go](file:///home/drusila/Projects/proxyma/internal/server/relay.go), [internal/server/nat.go](file:///home/drusila/Projects/proxyma/internal/server/nat.go), [internal/p2p/holepunch.go](file:///home/drusila/Projects/proxyma/internal/p2p/holepunch.go) o [internal/p2p/router.go](file:///home/drusila/Projects/proxyma/internal/p2p/router.go).
2. **Depuración de Errores de Conexión entre Nodos**: Cuando los nodos no logren conectarse directamente mTLS o fallen las peticiones `SubmitTask` / `DownloadBlob`.
3. **Optimización de Estrategias de Fallback**: Al ajustar intervalos de polling (`MinRelayPollInterval` vs `MaxRelayPollInterval`) o reintentos de perforación UDP QUIC.
4. **Implementación de Sondeos de Accesibilidad (`RequestProbe`)**: Al verificar si un nodo remoto es alcanzable directamente desde la red pública o requiere túnel Relay.

---

## 2. Archivos a Inspeccionar

* **Servidor y Relay Daemon**:
  - [internal/server/relay.go](file:///home/drusila/Projects/proxyma/internal/server/relay.go): Gestor de colas de peticiones tuneladas (`RelayManager`) y polling en segundo plano (`StartRelayPolling`).
  - [internal/server/nat.go](file:///home/drusila/Projects/proxyma/internal/server/nat.go): Descubrimiento automático de mapeo de puertos UPnP y NAT-PMP.
* **Malla P2P y Hole Punching**:
  - [internal/p2p/holepunch.go](file:///home/drusila/Projects/proxyma/internal/p2p/holepunch.go): Orquestación de perforación de puertos UDP y negociación de sesiones QUIC.
  - [internal/p2p/nat_mapper.go](file:///home/drusila/Projects/proxyma/internal/p2p/nat_mapper.go): Mapeo dinámico de interfaces de red locales e IPs públicas.
  - [internal/utils/stun.go](file:///home/drusila/Projects/proxyma/internal/utils/stun.go): Cliente STUN RFC 5389 para resolver IP/puerto público mapeado en el NAT.

---

## 3. Matriz Escalada de Conectividad P2P (Fallback Pipeline)

Proxyma resuelve la conectividad entre dos nodos probando progresivamente 4 mecanismos de menor a mayor latencia:

```
 ┌──────────────────────────────────────────────────────────┐
 │ Nivel 1: Conexión mTLS Directa (IP Pública / LAN)        │
 └────────────────────────────┬────────────────────────────┘
                              │ (Si falla o está tras NAT)
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ Nivel 2: Mapeo de Puertos Automático UPnP / NAT-PMP      │
 └────────────────────────────┬────────────────────────────┘
                              │ (Si el router no soporta UPnP)
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ Nivel 3: Perforación de Puertos UDP QUIC (Hole Punching) │
 └────────────────────────────┬────────────────────────────┘
                              │ (Si es NAT Simétrico / Firewall estricto)
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ Nivel 4: Túnel de Long-Polling Relay vía Nodo Sponsor    │
 └────────────────────────────┴────────────────────────────┘
```

### Detalle Operativo por Nivel:

1. **Nivel 1 (mTLS Directo)**: Petición HTTP/2 directa usando la dirección expuesta en `node_addr`.
2. **Nivel 2 (UPnP/PMP)**: Abrir puerto exterior en el gateway del usuario dinámicamente mediante `goupnp`.
3. **Nivel 3 (UDP Hole Punching)**: Intercambiar mensajes STUN `ProbeRequest` / `ProbeResponse` para sincronizar paquetes UDP simultáneos y abrir las tablas de estado del NAT de ambos lados.
4. **Nivel 4 (HTTP Long-Polling Relay)**:
   - El nodo detrás de NAT estricto mantiene una conexión de polling abierta contra un nodo sponsor accesible (`/cluster/relay/poll`).
   - Cuando un tercer nodo desea enviar una petición al nodo oculto, la encola en el `RelayManager` del sponsor (`queues[targetPeerID]`).
   - El sponsor responde al polling entregando la petición (`RelayRequest`). El nodo oculto la procesa localmente contra su propio `http.Handler` y devuelve la respuesta (`RelayResponse`) vía `/cluster/relay/reply`.

---

## 4. Guía de Diagnóstico y Depuración de Red

### A. Diagnosticar Fallos en el Túnel Relay (`RelayManager`)
1. **Verificar estado de colas**: Confirmar que el `peerID` destino esté registrado en `rm.queues` con capacidad buffer (máximo 10 peticiones).
2. **Evitar Race Conditions en Canales**: Al registrar canales de respuesta (`RegisterWaiter`), asegurar que tengan buffer de al menos 1 (`make(chan protocol.RelayResponse, 1)`) para evitar bloqueos si el emisor se desconecta antes de leer.
3. **Ajuste de Intervalo de Polling Adaptativo**:
   - `MinRelayPollInterval` (ej. 1s): Usar durante transferencia activa de datos.
   - `MaxRelayPollInterval` (ej. 10s): Usar cuando no haya peticiones pendientes para reducir overhead de CPU y red.

### B. Probar Sondeos de Accesibilidad (`RequestProbe`)
Antes de iniciar descargas pesadas o tareas de cómputo, enviar un `ProbeRequest` liviano al nodo remoto:
```go
probeResp, err := peerClient.RequestProbe(ctx, targetAddr, protocol.ProbeRequest{
    RequesterID: myNodeID,
    Timestamp:   time.Now().Unix(),
})
if err != nil || !probeResp.Reachable {
    // Escalar al Nivel 4 (Túnel Relay)
}
```

---
