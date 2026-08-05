package com.proxyma.android.utils

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import android.widget.Toast
import androidx.core.content.FileProvider
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import java.io.File
import java.io.FileOutputStream
import java.text.DecimalFormat
import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.DisposableEffect
import kotlin.concurrent.fixedRateTimer
import kotlin.concurrent.thread

fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0 B"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    val digitGroups = (Math.log10(bytes.toDouble()) / Math.log10(1000.0)).toInt()
    return DecimalFormat("#,##0.1").format(bytes / Math.pow(1000.0, digitGroups.toDouble())) + " " + units[digitGroups]
}

fun openFileNatively(context: Context, path: String, name: String) {
    try {
        val file = File(path)
        if (!file.exists()) {
            context.toast("Blob does not exist")
            return
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
        context.startActivity(Intent.createChooser(intent, "Open with"))
    } catch (e: Exception) {
        context.toast("Error opening file: ${e.message}", long = true)
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
    context.contentResolver.openInputStream(uri)?.use { input ->
        FileOutputStream(cacheFile).use { output ->
            input.copyTo(output)
        }
    }
    return cacheFile.absolutePath
}

fun createTempCameraFile(context: Context): Pair<Uri, File> {
    val photoFile = File(context.cacheDir, "camera_photo_${System.currentTimeMillis()}.jpg")
    val authority = "${context.packageName}.fileprovider"
    val uri = FileProvider.getUriForFile(context, authority, photoFile)
    return Pair(uri, photoFile)
}

fun isRunningOnMainThread(action: () -> Unit) {
    android.os.Handler(android.os.Looper.getMainLooper()).post(action)
}

fun parseJSONMap(json: String): Map<String, Any> {
    return try {
        Gson().fromJson<Map<String, Any>>(json, object : TypeToken<Map<String, Any>>() {}.type) ?: emptyMap()
    } catch (e: Exception) {
        emptyMap()
    }
}

fun getActionError(res: String): String = parseJSONField(res, "error")

/** Mirrors Go ParseBindError — sole Android entry for bind error envelopes. */
fun parseBindError(res: String): String = getActionError(res)

fun isBindError(res: String): Boolean = parseBindError(res).isNotEmpty()

fun getActionMessage(res: String): String = parseJSONField(res, "message")

fun getResultPath(res: String): String {
    val map = parseJSONMap(res)
    val outputs = map["outputs"] as? Map<String, Any>
    if (outputs != null) {
        val resultPath = outputs["result_path"] as? String
            ?: outputs["output_path"] as? String
            ?: outputs["note_path"] as? String
            ?: outputs["path"] as? String
        if (!resultPath.isNullOrEmpty() && !resultPath.startsWith("vfs://")) {
            return resultPath
        }
        val hash = outputs["output_hash"] as? String
        if (!hash.isNullOrEmpty()) {
            val local = proxyma_bind.Proxyma_bind.getLocalBlobPath(hash)
            if (local.isNotEmpty()) return local
        }
    }
    return ""
}

fun updateFileTask(
    fileTasks: MutableList<com.proxyma.android.models.FileTask>,
    taskId: String,
    transform: (com.proxyma.android.models.FileTask) -> com.proxyma.android.models.FileTask
) {
    val index = fileTasks.indexOfFirst { it.taskId == taskId }
    if (index != -1) {
        fileTasks[index] = transform(fileTasks[index])
    }
}

fun runBindOnBg(
    action: () -> String,
    onResult: (Result<String>) -> Unit
) {
    thread {
        try {
            val res = action()
            val err = parseBindError(res)
            isRunningOnMainThread {
                if (err.isNotEmpty()) {
                    onResult(Result.failure(Exception(err)))
                } else {
                    onResult(Result.success(res))
                }
            }
        } catch (e: Exception) {
            isRunningOnMainThread {
                onResult(Result.failure(e))
            }
        }
    }
}

fun startUnaryFileTask(
    fileTasks: MutableList<com.proxyma.android.models.FileTask>,
    taskId: String,
    context: Context? = null,
    action: () -> String,
    onDone: ((Result<String>) -> Unit)? = null
) {
    runBindOnBg(action) { result ->
        result.fold(
            onSuccess = { res ->
                val resPath = getResultPath(res).ifEmpty { null }
                updateFileTask(fileTasks, taskId) {
                    it.copy(status = "completed", resultPath = resPath)
                }
                context?.toast("✅ Execution completed!")
                onDone?.invoke(Result.success(res))
            },
            onFailure = { err ->
                val msg = err.message ?: "failed"
                updateFileTask(fileTasks, taskId) { it.copy(status = "failed", error = msg) }
                context?.toast("❌ Execution failed: $msg", long = true)
                onDone?.invoke(Result.failure(err))
            }
        )
    }
}

fun attachStreamToFileTask(
    fileTasks: MutableList<com.proxyma.android.models.FileTask>,
    taskId: String,
    serviceName: String,
    payloadJson: String,
    context: Context? = null,
    onDone: ((Result<String>) -> Unit)? = null
) {
    proxyma_bind.Proxyma_bind.streamService(serviceName, payloadJson, object : proxyma_bind.StreamEventListener {
        override fun onChunk(chunkJSON: String) {
            isRunningOnMainThread {
                updateFileTask(fileTasks, taskId) { curr ->
                    val updated = if (curr.streamOutput.isNullOrEmpty()) chunkJSON
                    else curr.streamOutput + "\n" + chunkJSON
                    curr.copy(streamOutput = updated)
                }
            }
        }

        override fun onError(errMsg: String) {
            isRunningOnMainThread {
                updateFileTask(fileTasks, taskId) { it.copy(status = "failed", error = errMsg) }
                context?.toast("❌ Stream error: $errMsg", long = true)
                onDone?.invoke(Result.failure(Exception(errMsg)))
            }
        }

        override fun onComplete() {
            isRunningOnMainThread {
                updateFileTask(fileTasks, taskId) { it.copy(status = "completed") }
                context?.toast("✅ Stream completed!")
                onDone?.invoke(Result.success("Streaming completed"))
            }
        }
    })
}

private fun parseJSONField(json: String, key: String): String {
    val map = parseJSONMap(json)
    return (map[key] as? String) ?: ""
}

@Composable
fun PollState(period: Long, action: () -> Unit) {
    DisposableEffect(Unit) {
        val timer = fixedRateTimer(period = period) {
            try {
                action()
            } catch (e: Exception) {
                e.printStackTrace()
            }
        }
        onDispose {
            timer.cancel()
        }
    }
}

@Composable
inline fun <reified T> rememberPolledParsedState(
    period: Long = 2000,
    initialValue: T,
    crossinline fetchData: () -> String
): State<T> {
    val state = remember { mutableStateOf(initialValue) }
    val gson = remember { Gson() }
    PollState(period = period) {
        if (proxyma_bind.Proxyma_bind.isNodeRunning()) {
            val res = fetchData()
            try {
                val parsed = gson.fromJson<T>(res, object : TypeToken<T>() {}.type)
                if (parsed != null) {
                    state.value = parsed
                }
            } catch (e: Exception) {
                // Suppress parsing errors during node starting phase
            }
        }
    }
    return state
}

fun uploadUriToVfs(
    context: Context,
    uri: Uri,
    onStart: () -> Unit,
    onComplete: (Result<String>) -> Unit
) {
    onStart()
    val name = getFileName(context, uri) ?: "upload_${System.currentTimeMillis()}"
    runBindOnBg({
        val cachedPath = copyUriToCache(context, uri)
        try {
            proxyma_bind.Proxyma_bind.uploadFile(name, cachedPath)
        } finally {
            File(cachedPath).delete()
        }
    }) { result ->
        result.fold(
            onSuccess = { res ->
                onComplete(Result.success(getActionMessage(res).ifEmpty { name }))
            },
            onFailure = { onComplete(Result.failure(it)) }
        )
    }
}

fun executeGoCall(
    context: Context,
    onStart: (() -> Unit)? = null,
    onComplete: (() -> Unit)? = null,
    action: () -> String,
    onSuccess: ((String) -> Unit)? = null
) {
    onStart?.invoke()
    runBindOnBg(action) { result ->
        onComplete?.invoke()
        result.fold(
            onSuccess = { res -> onSuccess?.invoke(res) },
            onFailure = { err -> context.toast(err.message ?: "Error", long = true) }
        )
    }
}

fun executeGoSubmit(
    onComplete: (Result<String>) -> Unit,
    action: () -> String,
    onSuccess: ((String) -> Unit)? = null
) {
    runBindOnBg(action) { result ->
        result.onSuccess { res -> onSuccess?.invoke(res) }
        onComplete(result)
    }
}

fun enqueueFileTask(
    fileTasks: MutableList<com.proxyma.android.models.FileTask>,
    name: String,
    payloadJson: String,
    streaming: Boolean,
    context: Context? = null,
    unaryAction: (() -> String)? = null,
    onDone: ((Result<String>) -> Unit)? = null
): String {
    val taskId = "task_${System.currentTimeMillis()}"
    fileTasks.add(
        0,
        com.proxyma.android.models.FileTask(
            taskId = taskId,
            service = name,
            input = payloadJson,
            output = if (streaming) "stream" else "result",
            status = if (streaming) "streaming" else "running",
            isStreaming = streaming
        )
    )
    if (streaming) {
        attachStreamToFileTask(fileTasks, taskId, name, payloadJson, context, onDone)
    } else {
        startUnaryFileTask(
            fileTasks = fileTasks,
            taskId = taskId,
            context = context,
            action = unaryAction ?: { """{"error":"missing unary action"}""" },
            onDone = onDone
        )
    }
    return taskId
}

/** Shared Gson parse of bind GetServiceDetails JSON (SSOT for screens). */
fun parseServiceDetail(raw: String): com.proxyma.android.models.ServiceDetail? {
    if (isBindError(raw)) return null
    return try {
        Gson().fromJson(raw, com.proxyma.android.models.ServiceDetail::class.java)
    } catch (_: Exception) {
        null
    }
}

fun parsePipelineSchema(json: String): com.proxyma.android.models.PipelineSchema? {
    return try {
        Gson().fromJson(json, com.proxyma.android.models.PipelineSchema::class.java)
    } catch (_: Exception) {
        null
    }
}

/** Empty fallback when schema has no parameters — never invent UI hints client-side. */
val DEFAULT_RUN_PARAMS = emptyList<com.proxyma.android.models.FormParameter>()

