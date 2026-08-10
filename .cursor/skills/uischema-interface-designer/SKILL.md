---
name: uischema-interface-designer
description: Diseñador de sistemas UI/UX y contratos declarativos de datos en Proxyma. Define la especificación uischema en JSON/YAML y guía la adaptación dinámica a interfaces CLI (Cobra), Android (Compose) y Desktop.
---

# UISchema Interface Designer - Proxyma

Esta skill define la especificación estándar del contrato declarativo **`uischema`** en Proxyma ([shared/uischema/uischema.go](file:///home/drusila/Projects/proxyma/shared/uischema/uischema.go)) y proporciona las reglas de transformación para renderizar dinámicamente cualquier acción admin del nodo en múltiples interfaces (CLI, Android, Desktop).

**SSOT scope:** admin domains/actions (storage, peers, cluster, telemetry, service-management). Compute labs (`clipboard.sync`, etc.) usan `protocol.ServiceSchema` / `services.json` — contrato paralelo, no mezclar.

Cada `ActionDetail` declara:
* `UnixAction` — string IPC del demonio (vacío = solo local, p.ej. `cluster.join`, `edit_pipeline`)
* `Hidden` — acciones IPC internas (`service.detail`, `service.stream`, `validate_pipeline`) excluidas de CLI/Android export
* `Surfaces` — opcional; restringe superficies UI

**Flujo de dispatch (interpreter genérico):**
```
Registry (domain.action + UnixAction + SuccessMessage)
  → Cobra: VisibleRegistry("cli")
  → CLI: NormalizeActionArgs → ValidateActionArgs → cliEscapes[key] OR InvokeDomainActionPrepared
  → Bind L3 (JNI/wrappers): NormalizeActionArgs → ValidateActionArgs → InvokeDomainActionPrepared
  → Bind L2 Prepared: offlineHooks? → CallUnixUnary / unix IPC
  → Daemon: unixHandlers[UnixAction]
```
Android (cuando exista UI): mismas piezas — `VisibleRegistry` + forms/`Parameters` + `ProjectRows` + **`InvokeDomainAction`**; wrappers tipados opcionales; **no** switch hardcodeado por `domain.action` en Compose.

Payload KV/JSON: `uischema.NormalizePayloadJSON` (SSOT; CLI `ParseInputsToJSON` es thin wrapper).

Añadir acción admin unary = **1 fila Registry + 1 `register(...)` en unix_handlers.go**. Cero líneas en CLI salvo escape UX. Brazo offline adicional = **1 entrada en `offlineHooks`** (bind). Tests: `TestUnixHandlersMatchRegistry`, `TestVisibleActionsEscapeXorUnix`.

---

## 1. Especificación Estándar del Esquema JSON/YAML (`uischema`)

El contrato `uischema` estructura las acciones expuestas por el nodo en **Dominios**, **Acciones**, **Parámetros** y **Columnas de Salida**.

### Ejemplo Completo en JSON
```json
{
  "name": "storage",
  "title": "Virtual File System & Storage",
  "actions": [
    {
      "domain": "storage",
      "name": "upload",
      "title": "Upload File",
      "description": "Upload a local file into the VFS registry",
      "outputType": "text",
      "parameters": [
        {
          "name": "path",
          "type": "string",
          "required": true,
          "description": "Absolute or relative path to the local file",
          "uiHint": "file_picker"
        },
        {
          "name": "name",
          "type": "string",
          "required": false,
          "description": "Optional destination filename inside VFS",
          "uiHint": "text"
        }
      ]
    },
    {
      "domain": "storage",
      "name": "list",
      "title": "List Files",
      "description": "List all files in the virtual file system snapshot",
      "outputType": "table",
      "columns": [
        { "header": "NAME", "fieldSelector": "name", "format": "string" },
        { "header": "SIZE", "fieldSelector": "size", "format": "bytes" },
        { "header": "STATUS", "fieldSelector": "deleted", "format": "status" }
      ]
    }
  ]
}
```

### Definición de Tipos y Opciones

#### Parámetros (`ParameterDetail`)
| Campo | Tipo | Valores Posibles / Descripción |
| :--- | :--- | :--- |
| `name` | `string` | Nombre técnico de la propiedad/parámetro |
| `type` | `string` | `"string"`, `"int"`, `"bool"`, `"file"` |
| `required` | `bool` | `true` si el parámetro es obligatorio |
| `description` | `string` | Explicación breve para el usuario |
| `uiHint` | `string` (opcional) | `"file_picker"`, `"image_picker"`, `"text"`, `"password"`, `"dropdown"` |
| `defaultValue` | `string` (opcional) | Valor por defecto en caso de no especificarse |
| `options` | `[]string` (opcional) | Lista de opciones cerradas (usado con `uiHint: "dropdown"`) |

#### Formatos de Salida (`outputType` & `TableColumn`)
* **`outputType`**:
  - `"table"`: Colección de objetos estructurados. Requiere definición de `columns`.
  - `"text"`: Mensajes de estado planos o confirmaciones simples.
  - `"json"`: Salida de datos crudos o respuestas complejas de microservicios.
* **`format` en `TableColumn`**:
  - `"string"`: Texto llano.
  - `"bytes"`: Conversión automática de bytes a formato legible (`1024` -> `"1.00 KB"`).
  - `"boolean"`: `true`/`false` formateado visualmente (`Yes`/`No` o Checkmark).
  - `"speed"`: Ancho de banda formateado (`"1.2 MB/s"`).
  - `"status"`: Estado del recurso (ej. `Online`/`Offline`, `Active`/`Deleted`).

---

## 2. Flujo de Validación y Generación de `uischema` (Para el Agente AI)

Cuando implementes o modifiques un servicio de cómputo (`ServiceSchema`), un pipeline (`PipelineSchema`) o un nuevo comando de demonio, sigue este flujo:

```
┌──────────────────────────────────────────────────────────┐
│ 1. Inspeccionar Handler y Parámetros del Servicio        │
└─────────────────────────────┬────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────┐
│ 2. Construir/Validar la Declaración `uischema`           │
└─────────────────────────────┬────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────┐
│ 3. Generar Mapeos para CLI, Android y Desktop            │
└─────────────────────────────┬────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────┐
│ 4. Registrar en `shared/uischema/uischema.go` o JSON     │
└─────────────────────────────┴────────────────────────────┘
```

### Checklist de Validación de Esquemas:
1. **Unicidad de Nombre**: El par `domain.action` debe ser único en el registro global.
2. **Coherencia de Tipos**: Si `uiHint: "file_picker"`, el `type` debe ser `"string"` o `"file"`.
3. **Completitud de Tabla**: Si `outputType: "table"`, debe definirse al menos una columna en `columns` y `fieldSelector` debe coincidir exactamente con las claves devueltas en la respuesta JSON.
4. **Parámetros Requeridos sin Valor por Defecto**: Verificar que todo parámetro `required: true` tenga una descripción clara y una estrategia de captura en cada interfaz.

---

## 3. Adaptadores de Interfaz Multiplataforma

### A. Adaptador CLI (Cobra & Terminal TUI)

Mapea la declaración `uischema` a la línea de comandos ejecutable en Go:

1. **Banderas y Parámetros**:
   - `type: "string"` -> `cmd.Flags().StringVar(&val, name, defaultVal, desc)`
   - `type: "int"` -> `cmd.Flags().IntVar(&val, name, defaultVal, desc)`
   - `type: "bool"` -> `cmd.Flags().BoolVar(&val, name, defaultVal, desc)`
   - `required: true` -> `cmd.MarkFlagRequired(name)`
   - `uiHint: "file_picker"` -> Autocompletado de archivos en la terminal (`cmd.MarkFlagFilename(name)`).

2. **Modo Interactivo (Prompts)**:
   - Si se ejecuta en una TUI/Terminal interactiva y falta un parámetro obligatorio, solicitarlo dinámicamente mediante prompt de texto o selector de lista.

3. **Formateo de Salida**:
   - `outputType: "table"` -> Renderizar usando `text/tabwriter` o tabla Markdown formateando automáticamente columnas `"bytes"` y `"status"`.
   - `outputType: "json"` -> `json.MarshalIndent(data, "", "  ")`.

### B. Adaptador Android (Jetpack Compose)

**Contrato interpreter (prioridad):** pantallas caminan `VisibleRegistry(surface)` + invocan **`InvokeDomainAction`**; tablas vía `ProjectRows` / `FormatBytes`. No duplicar el Registry en switches Compose por `domain.action`. Wrappers tipados (`UploadFile`, …) son L3 opcionales para JNI.

Mapea el esquema a componentes de UI móviles táctiles:

1. **Parámetros a Composables**:
   - `uiHint: "file_picker"` -> Botón con icono de carpeta que lanza `rememberLauncherForActivityResult(ActivityResultContracts.GetContent())`.
   - `uiHint: "dropdown"` -> `ExposedDropdownMenuBox` renderizando las opciones definidas en `options`.
   - `uiHint: "password"` -> `OutlinedTextField` con `VisualTransformation.Password`.
   - `type: "bool"` -> `Row` con `Switch` interactivo y etiqueta `title`.
   - `type: "int"` -> `OutlinedTextField` con `keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number)`.

2. **Renderizado de Salidas**:
   - `outputType: "table"` -> `LazyColumn` / filas proyectadas con `ProjectRows` (chips de color según formato `"status"`).

### C. Adaptador Desktop (GUI / Windows / Forms)

Mapea el esquema a controles gráficos nativos de escritorio:

1. **Parámetros**:
   - `uiHint: "file_picker"` -> `TextBox` flanqueado por un botón `"Examinar..."` que abre `OpenFileDialog`.
   - `uiHint: "dropdown"` -> `ComboBox` con selección fija.
   - `type: "bool"` -> CheckBox o Toggle Switch.

2. **Renderizado de Salidas**:
   - `outputType: "table"` -> `DataGrid` ordenable por cabecera de columna con ajuste automático de ancho.

---

## 4. Alineación con `protocol.ServiceParameter`

Los servicios de compute usan `ServiceParameter.ui_hint` (`file_picker` / `image_picker`) en el schema del servicio. Bind expone eso en `ParameterDetail.uiHint` hacia Android. **No** reintroducir heurísticas por nombre de parámetro en Compose si el hint ya viene del schema.

Tras cambiar uischema o convenciones de hints: actualizar `.cursorrules.md`, `.agents/AGENTS.md` y esta skill.
