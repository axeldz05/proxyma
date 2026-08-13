---
name: android-gomobile-orchestrator
description: Guía para la compilación gomobile, integración JNI/Kotlin Compose y despliegue autónomo por ADB en el proyecto Android de Proxyma (proxyma-bind y proxyma-android).
---

# Android GoMobile Orchestrator - Proxyma

Esta skill proporciona las directrices técnicas, restricciones de tipos y flujo de trabajo para compilar la biblioteca GoMobile (`proxyma.aar`), integrarla en la app nativa Android en Kotlin Jetpack Compose (`cmd/proxyma-android`) y desplegarla dinámicamente vía ADB.

---

## 1. Triggers (Cuándo usar esta Skill)

Activa y consulta esta skill en los siguientes escenarios:
1. **Modificación de la interfaz GoMobile**: Al alterar o agregar funciones expuestas en [cmd/proxyma-bind/bind.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/bind.go) o sus módulos auxiliares (`cluster.go`, `service.go`, `storage.go`).
2. **Desarrollo de UI Móvil Android**: Al modificar o construir pantallas en Kotlin (`ServicesScreen.kt`, `PipelineEditor.kt`) bajo el patrón de Granularidad Continua de 3 Niveles.
3. **Despliegue y Pruebas en Dispositivo Físico**: Cuando necesites recompilar `proxyma.aar`, generar la APK con Gradle e instalarla dinámicamente usando ADB.
4. **Depuración de Errores JNI o Crashes en Android**: Al analizar fallos de CGO, panics en Go invocados desde Java/Kotlin o inspeccionar logs de `adb logcat`.

---

## 2. Archivos a Inspeccionar

* **Capa Bindings GoMobile**:
  - [cmd/proxyma-bind/bind.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/bind.go): Inicialización del nodo móvil, logger y ciclo de vida.
  - [cmd/proxyma-bind/service.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/service.go): Bindings para agregar, ejecutar y listar servicios y pipelines.
  - [cmd/proxyma-bind/cluster.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/cluster.go): Bindings de invitación, enrolamiento `join` y estado del clúster.
  - [cmd/proxyma-bind/storage.go](file:///home/drusila/Projects/proxyma/cmd/proxyma-bind/storage.go): Bindings para listado VFS, subida y suscripción de archivos.
* **Capa Nativa Android**:
  - [cmd/proxyma-android/ship_to_attached_phone.sh](file:///home/drusila/Projects/proxyma/cmd/proxyma-android/ship_to_attached_phone.sh): Script maestro de compilación `gomobile bind` + `gradle assembleDebug` + `adb install`.
  - [cmd/proxyma-android/app/build.gradle.kts](file:///home/drusila/Projects/proxyma/cmd/proxyma-android/app/build.gradle.kts): Dependencia de la AAR local (`app/libs/proxyma.aar`).

---

## 3. Reglas Técnicas y Restricciones de gomobile

### A. Tipos de Datos Soportados por gomobile
`gomobile bind` impone restricciones estrictas sobre los tipos de parámetros en funciones exportadas en el paquete `main` o paquete de binding:
* **Soportados**: `string`, `bool`, `int`, `int64`, `[]byte`, e interfaces Go sencillas que retornen `(T, error)` o `error`.
* **NO Soportados**: Maps genéricos (`map[string]any`), structs complejas exportadas con punteros anidados o slices de structs personalizadas.
* **Regla de Conversión**: Cualquier objeto complejo (ej. `ServiceSchema`, respuestas de pipeline) **DEBE** ser serializado como un `string` en formato JSON en Go y deserializado en Kotlin mediante `JSONObject` o `kotlinx.serialization`.

### B. Hilos UI y Seguridad Corrutinas en Kotlin
* **NUNCA** invoques funciones pesadas de `proxyma-bind` (ej. `RunService`, `JoinCluster`, `SyncVFS`) directamente en el hilo principal de UI (`MainThread`) de Android.
* **DEBES** envolver siempre las llamadas a la AAR en `withContext(Dispatchers.IO)` o `lifecycleScope.launch(Dispatchers.IO)`.

---

## 4. Workflow de Compilación y Despliegue ADB (Paso a Paso)

```
 ┌──────────────────────────────────────────────────────────┐
 │ 1. Configurar Entorno (`ANDROID_HOME`, `ANDROID_NDK`)   │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 2. Validar Dispositivo Físico USB (`adb devices -l`)     │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 3. Recompilar GoMobile AAR (`gomobile bind`)             │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 4. Compilar APK Debug (`gradle assembleDebug`)           │
 └────────────────────────────┬────────────────────────────┘
                              │
                              ▼
 ┌──────────────────────────────────────────────────────────┐
 │ 5. Inyectar y Ver de Logcat (`adb install` + `logcat`)   │
 └────────────────────────────┴────────────────────────────┘
```

### Ejecución del Script de Automatización:
```bash
# Navegar al directorio de Android
cd cmd/proxyma-android

# Otorgar ejecución y lanzar el despliegue automático
chmod +x ship_to_attached_phone.sh
./ship_to_attached_phone.sh
```

### Comandos Individuales de Debugging:

`go.mod` fija tanto `gomobile` como `gobind`. Como `gomobile bind` busca
`gobind` por `PATH`, resuelve el ejecutable fijado con `go tool -n`; no uses
`gomobile init` ni instalaciones `@latest`, porque desacoplan ambas versiones.

1. **Recompilar solo la AAR**:
   ```bash
   gobind_dir="$(dirname "$(go tool -n gobind)")"
   PATH="$gobind_dir:$PATH" go tool gomobile bind \
     -o cmd/proxyma-android/app/libs/proxyma.aar \
     -target=android -androidapi=21 ./cmd/proxyma-bind
   ```
2. **Limpiar y Filtrar Logs del Celular en Tiempo Real**:
   ```bash
   adb logcat -c
   adb logcat | grep com.proxyma.android
   ```

---
