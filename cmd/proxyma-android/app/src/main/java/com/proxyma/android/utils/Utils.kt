package com.proxyma.android.utils

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import android.widget.Toast
import androidx.core.content.FileProvider
import com.google.gson.Gson
import com.google.gson.JsonObject
import com.google.gson.JsonParser
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.FormParameter
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.lang.reflect.InvocationTargetException
import java.text.DecimalFormat
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.State
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberUpdatedState
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0 B"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    val digitGroups = (Math.log10(bytes.toDouble()) / Math.log10(1000.0)).toInt()
    return DecimalFormat("#,##0.1").format(bytes / Math.pow(1000.0, digitGroups.toDouble())) + " " + units[digitGroups]
}

fun openFileNatively(
    scope: CoroutineScope,
    context: Context,
    path: String,
    name: String
) {
    runOnBg(scope, action = {
        val file = File(path)
        if (!file.exists()) {
            throw IllegalStateException("Blob does not exist")
        }

        val isPdf = try {
            file.inputStream().use { input ->
                val header = ByteArray(4)
                val read = input.read(header)
                read == 4 && header[0] == '%'.code.toByte() && header[1] == 'P'.code.toByte() && header[2] == 'D'.code.toByte() && header[3] == 'F'.code.toByte()
            }
        } catch (e: Exception) {
            false
        }

        val ext = if (isPdf) "pdf" else name.substringAfterLast('.', "")
        val safeName = if (ext.isNotEmpty()) "result.$ext" else "result"
        val cacheSharedFile = File(context.cacheDir, safeName)

        file.inputStream().use { input ->
            cacheSharedFile.outputStream().use { output ->
                input.copyTo(output)
            }
        }

        val authority = "${context.packageName}.fileprovider"
        val uri = FileProvider.getUriForFile(context, authority, cacheSharedFile)

        val mimeType = context.contentResolver.getType(uri) ?: "*/*"
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mimeType)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        Intent.createChooser(intent, "Open with")
    }) { prepared ->
        consumePrepared(prepared, context::startActivity).onFailure { error ->
            context.toast("Error opening file: ${error.message}", long = true)
        }
    }
}

fun Context.toast(message: String, long: Boolean = false) {
    Toast.makeText(this, message, if (long) Toast.LENGTH_LONG else Toast.LENGTH_SHORT).show()
}

fun getFileName(context: Context, uri: Uri): String? {
    var result: String? = null
    if (uri.scheme == "content") {
        val cursor = context.contentResolver.query(uri, null, null, null, null)
        cursor?.use {
            if (it.moveToFirst()) {
                val index = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (index != -1) {
                    result = it.getString(index)
                }
            }
        }
    }
    if (result == null) {
        result = uri.path
        val cut = result?.lastIndexOf('/') ?: -1
        if (cut != -1) {
            result = result?.substring(cut + 1)
        }
    }
    return result
}

fun copyUriToCache(context: Context, uri: Uri): String {
    val name = getFileName(context, uri) ?: "input_${System.currentTimeMillis()}"
    val cacheFile = File(context.cacheDir, name)
    requireContentStream(
        context.contentResolver.openInputStream(uri),
        uri.toString()
    ).use { input ->
        FileOutputStream(cacheFile).use { output ->
            input.copyTo(output)
        }
    }
    return cacheFile.absolutePath
}

fun requireContentStream(stream: InputStream?, source: String): InputStream =
    stream ?: throw IOException("Unable to open content stream: $source")

fun <T> consumePrepared(
    prepared: Result<T>,
    consume: (T) -> Unit
): Result<Unit> = prepared.fold(
    onSuccess = { value -> runCatching { consume(value) } },
    onFailure = { error -> Result.failure(error) }
)

fun createTempCameraFile(context: Context): Pair<Uri, File> {
    val photoFile = File(context.cacheDir, "camera_photo_${System.currentTimeMillis()}.jpg")
    val authority = "${context.packageName}.fileprovider"
    val uri = FileProvider.getUriForFile(context, authority, photoFile)
    return Pair(uri, photoFile)
}

/**
 * Android-side interpretation of the Go bind error envelope.
 *
 * Successful JSON payloads, including {"error":""}, remain successful.
 */
fun parseBindError(response: String): String {
    val root = parseJson(response) ?: return ""
    if (!root.isJsonObject) return ""

    val error = root.asJsonObject.get("error") ?: return ""
    if (error.isJsonNull) return ""
    return if (error.isJsonPrimitive && error.asJsonPrimitive.isString) {
        error.asString
    } else {
        error.toString()
    }
}

fun isBindError(response: String): Boolean = parseBindError(response).isNotBlank()

enum class BindMethod {
    JSON_ENVELOPE,
    LEGACY_ERROR_PREFIX,
    START_NODE
}

fun bindResult(
    response: String,
    method: BindMethod = BindMethod.JSON_ENVELOPE
): Result<String> {
    val error = parseBindError(response)
    if (error.isNotBlank()) {
        return Result.failure(BindCallException(error.trim()))
    }

    // A valid JSON response without a meaningful error field is current-format success.
    if (parseJson(response) != null) {
        return Result.success(response)
    }

    val trimmed = response.trim()
    val prefixedError = legacyErrorPrefix(trimmed)
    return when {
        method == BindMethod.START_NODE && trimmed.isNotEmpty() ->
            Result.failure(BindCallException(prefixedError ?: trimmed))
        method == BindMethod.LEGACY_ERROR_PREFIX && prefixedError != null ->
            Result.failure(BindCallException(prefixedError))
        else -> Result.success(response)
    }
}

class BindCallException(message: String) : Exception(message)

private fun parseJson(response: String) = try {
    JsonParser.parseString(response)
} catch (_: Exception) {
    null
}

private fun legacyErrorPrefix(response: String): String? {
    if (!response.startsWith("error:", ignoreCase = true)) return null
    return response.substringAfter(':').trim().ifEmpty { "bind call failed" }
}

fun bindErrorMessage(
    response: String,
    method: BindMethod = BindMethod.LEGACY_ERROR_PREFIX
): String = bindResult(response, method).exceptionOrNull()?.message
    ?: response.trim().ifEmpty { "bind call failed" }

data class VfsUploadResult(
    val logicalName: String,
    val message: String
)

fun normalizeVfsUploadResult(
    logicalName: String,
    response: String
): Result<VfsUploadResult> = bindResult(
    response,
    BindMethod.LEGACY_ERROR_PREFIX
).map {
    VfsUploadResult(
        logicalName = logicalName,
        message = getActionMessage(response).ifBlank { "Uploaded '$logicalName'" }
    )
}

enum class StopBindingMode {
    STOP_NODE_WITH_ERROR,
    LEGACY_STOP_NODE
}

data class NodeStopResult(
    val mode: StopBindingMode
)

class ReflectiveNodeStopApi(
    bindingClass: Class<*>,
    private val receiver: Any? = null
) {
    private val stopWithErrorMethod = bindingClass.methods.firstOrNull {
        it.name == "stopNodeWithError" && it.parameterCount == 0
    }?.apply { isAccessible = true }
    private val legacyStopMethod = bindingClass.methods.firstOrNull {
        it.name == "stopNode" && it.parameterCount == 0
    }?.apply { isAccessible = true }

    fun stop(): Result<NodeStopResult> = runCatching {
        val modernMethod = stopWithErrorMethod
        if (modernMethod != null) {
            val response = invoke(modernMethod) as? String
                ?: throw UnsupportedOperationException(
                    "StopNodeWithError does not return String"
                )
            bindResult(response, BindMethod.LEGACY_ERROR_PREFIX).getOrThrow()
            return@runCatching NodeStopResult(StopBindingMode.STOP_NODE_WITH_ERROR)
        }

        val legacyMethod = legacyStopMethod
            ?: throw UnsupportedOperationException(
                "Bundled proxyma.aar lacks StopNodeWithError and legacy StopNode"
            )
        invoke(legacyMethod)
        NodeStopResult(StopBindingMode.LEGACY_STOP_NODE)
    }

    private fun invoke(method: java.lang.reflect.Method): Any? = try {
        method.invoke(receiver)
    } catch (error: InvocationTargetException) {
        throw (error.targetException as? Exception ?: error)
    }
}

class ReflectiveBindStreamApi(
    bindingClass: Class<*>,
    private val receiver: Any? = null
) {
    private val streamMethod = bindingClass.methods.firstOrNull {
        it.name == "streamService" && it.parameterCount == 3
    }?.apply { isAccessible = true }
    private val cancelMethod = bindingClass.methods.firstOrNull {
        it.name == "cancelStream" && it.parameterCount == 1
    }?.apply { isAccessible = true }

    val supportsCancellableStreams: Boolean =
        streamMethod?.returnType == String::class.java &&
            cancelMethod?.returnType == String::class.java

    fun start(name: String, payloadJson: String, listener: Any): Result<StreamLease> =
        runCatching {
            if (!supportsCancellableStreams) {
                throw UnsupportedOperationException(
                    "Bundled proxyma.aar lacks cancellable StreamService/CancelStream APIs"
                )
            }
            val response = invokeString(streamMethod, name, payloadJson, listener)
            val normalized = bindResult(
                response,
                BindMethod.LEGACY_ERROR_PREFIX
            ).getOrThrow()
            val streamID = parseStreamID(normalized)
                ?: throw BindCallException("StreamService response is missing stream_id")
            StreamLease(streamID, this)
        }

    internal fun cancel(streamID: String): Result<String> = runCatching {
        if (!supportsCancellableStreams) {
            throw UnsupportedOperationException(
                "Bundled proxyma.aar lacks CancelStream"
            )
        }
        val response = invokeString(cancelMethod, streamID)
        bindResult(response, BindMethod.LEGACY_ERROR_PREFIX).getOrThrow()
    }

    private fun invokeString(method: java.lang.reflect.Method?, vararg args: Any): String {
        val resolved = method ?: throw UnsupportedOperationException("Bind method unavailable")
        return try {
            resolved.invoke(receiver, *args) as? String
                ?: throw UnsupportedOperationException("Bind method does not return String")
        } catch (error: InvocationTargetException) {
            throw (error.targetException as? Exception ?: error)
        }
    }
}

class StreamLease internal constructor(
    val streamId: String,
    private val api: ReflectiveBindStreamApi
) {
    private val canceled = AtomicBoolean(false)

    fun cancel(): Result<String> {
        if (!canceled.compareAndSet(false, true)) {
            return Result.success("")
        }
        return api.cancel(streamId)
    }
}

private fun parseStreamID(response: String): String? {
    val root = parseJson(response)?.takeIf { it.isJsonObject }?.asJsonObject
        ?: return null
    val streamID = root.get("stream_id") ?: return null
    return streamID.takeIf {
        it.isJsonPrimitive && it.asJsonPrimitive.isString
    }?.asString?.takeIf { it.isNotBlank() }
}

private val defaultBindStreamApi by lazy {
    ReflectiveBindStreamApi(proxyma_bind.Proxyma_bind::class.java)
}

fun launchManagedBindStream(
    scope: CoroutineScope,
    serviceName: String,
    payloadJson: String,
    listenerFactory: (stop: () -> Unit) -> Any,
    onStarted: (streamID: String) -> Unit = {},
    onStartFailure: (Throwable) -> Unit = {},
    api: ReflectiveBindStreamApi = defaultBindStreamApi,
    callbackDispatcher: CoroutineDispatcher = Dispatchers.Main.immediate
): Job {
    lateinit var job: Job
    job = scope.launch(Dispatchers.IO, start = CoroutineStart.LAZY) {
        var lease: StreamLease? = null
        try {
            val listener = listenerFactory { job.cancel() }
            val startedLease = api
                .start(serviceName, payloadJson, listener)
                .getOrThrow()
            lease = startedLease
            withContext(callbackDispatcher) {
                onStarted(startedLease.streamId)
            }
            awaitCancellation()
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Throwable) {
            withContext(callbackDispatcher) {
                onStartFailure(error)
            }
        } finally {
            withContext(NonCancellable + Dispatchers.IO) {
                lease?.let { activeLease ->
                    activeLease.cancel().onFailure { error ->
                        android.util.Log.e(
                            "ProxymaStream",
                            "CancelStream failed for ${activeLease.streamId}",
                            error
                        )
                    }
                }
            }
        }
    }
    job.start()
    return job
}

enum class DaemonState {
    STOPPED,
    STARTING,
    RUNNING,
    STOPPING,
    ERROR
}

enum class StopRequest {
    EXECUTE,
    WAIT,
    ALREADY_STOPPED
}

class DaemonStateMachine {
    private val state = AtomicReference(DaemonState.STOPPED)

    val current: DaemonState
        get() = state.get()

    fun requestStart(): Boolean =
        state.compareAndSet(DaemonState.STOPPED, DaemonState.STARTING)

    fun markStarted(): Boolean =
        state.compareAndSet(DaemonState.STARTING, DaemonState.RUNNING)

    fun markStartFailed() {
        state.compareAndSet(DaemonState.STARTING, DaemonState.STOPPED)
    }

    fun requestStop(): StopRequest {
        while (true) {
            when (val current = state.get()) {
                DaemonState.STOPPED -> return StopRequest.ALREADY_STOPPED
                DaemonState.STOPPING -> return StopRequest.WAIT
                DaemonState.STARTING,
                DaemonState.RUNNING,
                DaemonState.ERROR -> {
                    if (state.compareAndSet(current, DaemonState.STOPPING)) {
                        return StopRequest.EXECUTE
                    }
                }
            }
        }
    }

    fun markStopped() {
        state.set(DaemonState.STOPPED)
    }

    fun markStopFailed() {
        state.compareAndSet(DaemonState.STOPPING, DaemonState.ERROR)
    }
}

data class TaskResultReference(
    val localPath: String? = null,
    val blobHash: String? = null
)

/**
 * Pure counterpart of ResolveTaskResultPath for Android builds whose checked-in
 * AAR predates that binding. Any blob lookup is performed separately on IO.
 */
fun parseTaskResultReference(response: String): TaskResultReference {
    val root = try {
        JsonParser.parseString(response).asJsonObject
    } catch (_: Exception) {
        return TaskResultReference()
    }
    val outputs = root.objectField("outputs")
        ?: root.objectField("data")?.objectField("outputs")
        ?: return TaskResultReference()

    for (key in listOf("result_path", "output_path")) {
        val path = outputs.stringField(key)
        if (!path.isNullOrBlank() && !path.startsWith("vfs://")) {
            return TaskResultReference(localPath = path)
        }
    }

    val explicitHash = outputs.stringField("output_hash")
    if (!explicitHash.isNullOrBlank()) {
        return TaskResultReference(blobHash = explicitHash)
    }
    outputs.entrySet().forEach { (_, value) ->
        if (value.isJsonPrimitive && value.asJsonPrimitive.isString) {
            val candidate = value.asString
            if (candidate.startsWith("vfs://") && candidate.length > "vfs://".length) {
                return TaskResultReference(blobHash = candidate.removePrefix("vfs://"))
            }
        }
    }
    return TaskResultReference()
}

private fun JsonObject.objectField(name: String): JsonObject? {
    val value = get(name) ?: return null
    return value.takeIf { it.isJsonObject }?.asJsonObject
}

private fun JsonObject.stringField(name: String): String? {
    val value = get(name) ?: return null
    return value.takeIf {
        it.isJsonPrimitive && it.asJsonPrimitive.isString
    }?.asString
}

fun parseJSONMap(json: String): Map<String, Any> {
    return try {
        Gson().fromJson<Map<String, Any>>(json, object : TypeToken<Map<String, Any>>() {}.type) ?: emptyMap()
    } catch (e: Exception) {
        emptyMap()
    }
}

fun getActionMessage(res: String): String = parseJSONField(res, "message")

fun <T> runOnBg(
    scope: CoroutineScope,
    action: () -> T,
    onResult: (Result<T>) -> Unit
) {
    scope.launch {
        val result = try {
            Result.success(withContext(Dispatchers.IO) { action() })
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            Result.failure(error)
        }
        withContext(Dispatchers.Main.immediate) {
            onResult(result)
        }
    }
}

fun runBindOnBg(
    scope: CoroutineScope,
    action: () -> String,
    method: BindMethod = BindMethod.LEGACY_ERROR_PREFIX,
    onResult: (Result<String>) -> Unit
) {
    runOnBg(
        scope = scope,
        action = { bindResult(action(), method).getOrThrow() },
        onResult = onResult
    )
}

private fun parseJSONField(json: String, key: String): String {
    val map = parseJSONMap(json)
    return (map[key] as? String) ?: ""
}

@Composable
fun <T> PollState(
    period: Long,
    fetchData: () -> T,
    onResult: (T) -> Unit
) {
    val currentFetch = rememberUpdatedState(fetchData)
    val currentOnResult = rememberUpdatedState(onResult)
    LaunchedEffect(period) {
        while (isActive) {
            try {
                val value = withContext(Dispatchers.IO) {
                    currentFetch.value()
                }
                currentOnResult.value(value)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                // The daemon may be between lifecycle states; keep the last good value.
            }
            delay(period.coerceAtLeast(1L))
        }
    }
}

@Composable
inline fun <reified T> rememberPolledParsedState(
    period: Long = 2000,
    initialValue: T,
    noinline fetchData: () -> String
): State<T> {
    val state = remember { mutableStateOf(initialValue) }
    val gson = remember { Gson() }
    val type = remember { object : TypeToken<T>() {}.type }
    PollState(
        period = period,
        fetchData = {
            if (!proxyma_bind.Proxyma_bind.isNodeRunning()) {
                null
            } else {
                val response = bindResult(
                    fetchData(),
                    BindMethod.LEGACY_ERROR_PREFIX
                ).getOrThrow()
                gson.fromJson<T>(response, type)
            }
        },
        onResult = { parsed ->
            if (parsed != null) {
                state.value = parsed
            }
        }
    )
    return state
}

fun uploadUriToVfs(
    scope: CoroutineScope,
    context: Context,
    uri: Uri,
    onStart: () -> Unit,
    onComplete: (Result<VfsUploadResult>) -> Unit
) {
    onStart()
    runOnBg(scope, action = {
        val name = getFileName(context, uri) ?: "upload_${System.currentTimeMillis()}"
        val cachedPath = copyUriToCache(context, uri)
        try {
            normalizeVfsUploadResult(
                logicalName = name,
                response = proxyma_bind.Proxyma_bind.uploadFile(name, cachedPath)
            ).getOrThrow()
        } finally {
            File(cachedPath).delete()
        }
    }, onResult = onComplete)
}

fun executeGoCall(
    scope: CoroutineScope,
    context: Context,
    onStart: (() -> Unit)? = null,
    onComplete: (() -> Unit)? = null,
    action: () -> String,
    onSuccess: ((String) -> Unit)? = null
) {
    executeGoSubmit(
        scope = scope,
        onComplete = { result ->
            onComplete?.invoke()
            result.onFailure { err -> context.toast(err.message ?: "Error", long = true) }
        },
        action = action,
        onSuccess = onSuccess,
        onStart = onStart
    )
}

fun executeGoSubmit(
    scope: CoroutineScope,
    onComplete: (Result<String>) -> Unit,
    action: () -> String,
    onSuccess: ((String) -> Unit)? = null,
    onStart: (() -> Unit)? = null
) {
    onStart?.invoke()
    runBindOnBg(scope, action) { result ->
        result.onSuccess { res -> onSuccess?.invoke(res) }
        onComplete(result)
    }
}

/** Map a uischema/bind parameter map into FormParameter (L2). */
fun formParameterFrom(param: Map<String, Any?>, nameOverride: String? = null): FormParameter {
    return FormParameter(
        name = nameOverride ?: (param["name"] as? String) ?: "",
        type = (param["type"] as? String) ?: "string",
        required = (param["required"] as? Boolean) ?: false,
        description = (param["description"] as? String) ?: "",
        uiHint = param["uiHint"] as? String,
        defaultValue = param["defaultValue"] as? String,
        options = (param["options"] as? List<*>)
            ?.mapNotNull { it as? String }
            .orEmpty()
    )
}

/** Build FormParameter from an existing FormParameter / ServiceDetail param (L2). */
fun formParameterFrom(
    src: FormParameter?,
    name: String,
    fallbackType: String = "string"
): FormParameter {
    return FormParameter(
        name = name,
        type = src?.type ?: fallbackType,
        required = src?.required ?: false,
        description = src?.description ?: "",
        uiHint = src?.uiHint,
        defaultValue = src?.defaultValue,
        options = src?.options.orEmpty()
    )
}

/** Shared Gson parse of bind GetServiceDetails JSON (SSOT for screens). */
fun parseServiceDetail(raw: String): com.proxyma.android.models.ServiceDetail? {
    if (isBindError(raw)) return null
    return try {
        val parsed = Gson().fromJson(raw, com.proxyma.android.models.ServiceDetail::class.java)
            ?: return null
        parsed.copy(
            requiredPermissions = parsed.requiredPermissions.orEmpty(),
            parameters = parsed.parameters.orEmpty().map { parameter ->
                parameter.copy(options = parameter.options.orEmpty())
            },
            outputs = parsed.outputs.orEmpty().mapValues { (_, parameter) ->
                parameter.copy(options = parameter.options.orEmpty())
            }
        )
    } catch (_: Exception) {
        null
    }
}

/** L1: sync ServiceDetail fetch via bind (call from bg thread). */
fun fetchServiceDetail(name: String): com.proxyma.android.models.ServiceDetail? {
    val raw = proxyma_bind.Proxyma_bind.getServiceDetails(name)
    val normalized = bindResult(raw, BindMethod.LEGACY_ERROR_PREFIX).getOrNull()
        ?: return null
    return parseServiceDetail(normalized)
}

/** Background load of ServiceDetail via bind (L3). */
fun loadServiceDetail(
    scope: CoroutineScope,
    name: String,
    onResult: (com.proxyma.android.models.ServiceDetail?) -> Unit
) {
    runBindOnBg(scope, { proxyma_bind.Proxyma_bind.getServiceDetails(name) }) { result ->
        onResult(result.getOrNull()?.let { parseServiceDetail(it) })
    }
}

/** Background batch load of ServiceDetail map (L3). */
fun loadServiceDetailsMap(
    scope: CoroutineScope,
    names: List<String>,
    onResult: (Map<String, com.proxyma.android.models.ServiceDetail>) -> Unit
) {
    runOnBg(scope, action = {
        val map = mutableMapOf<String, com.proxyma.android.models.ServiceDetail>()
        for (svc in names.distinct()) {
            if (svc.isNotEmpty()) {
                fetchServiceDetail(svc)?.let { map[svc] = it }
            }
        }
        map.toMap()
    }) { result ->
        onResult(result.getOrDefault(emptyMap()))
    }
}

/** Background load of run-dialog parameter specs + streaming flag (L3). */
fun loadRunSpecs(
    scope: CoroutineScope,
    name: String,
    onResult: (specs: List<com.proxyma.android.models.FormParameter>, isStreaming: Boolean) -> Unit
) {
    loadServiceDetail(scope, name) { detail ->
        val specs = detail?.parameters?.takeIf { it.isNotEmpty() } ?: DEFAULT_RUN_PARAMS
        onResult(specs, detail?.isStreaming == true)
    }
}

fun taskStatusColor(status: String): androidx.compose.ui.graphics.Color = when (status) {
    "completed" -> com.proxyma.android.ui.theme.MintGreen
    "failed" -> androidx.compose.ui.graphics.Color.Red
    else -> androidx.compose.ui.graphics.Color.Yellow
}

fun parsePipelineSchema(json: String): com.proxyma.android.models.PipelineSchema? {
    if (isBindError(json)) return null
    return try {
        val parsed = Gson().fromJson(json, com.proxyma.android.models.PipelineSchema::class.java)
            ?: return null
        parsed.copy(
            steps = parsed.steps.orEmpty(),
            connections = parsed.connections.orEmpty()
        )
    } catch (_: Exception) {
        null
    }
}

/** Empty fallback when schema has no parameters — never invent UI hints client-side. */
val DEFAULT_RUN_PARAMS = emptyList<com.proxyma.android.models.FormParameter>()

