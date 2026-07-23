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

fun getActionError(res: String): String = parseJSONField(res, "error")

fun getActionMessage(res: String): String = parseJSONField(res, "message")

fun getResultPath(res: String): String {
    return try {
        val map = Gson().fromJson<Map<String, Any>>(res, object : TypeToken<Map<String, Any>>() {}.type)
        val outputs = map["outputs"] as? Map<String, Any>
        if (outputs != null) {
            val resultPath = outputs["result_path"] as? String
                ?: outputs["output_path"] as? String
                ?: outputs["note_path"] as? String
                ?: outputs["path"] as? String
            if (!resultPath.isNullOrEmpty() && !resultPath.startsWith("vfs://")) {
                return resultPath
            }
        }
        ""
    } catch (e: Exception) {
        ""
    }
}

private fun parseJSONField(json: String, key: String): String {
    return try {
        val map = Gson().fromJson<Map<String, Any>>(json, object : TypeToken<Map<String, Any>>() {}.type)
        (map[key] as? String) ?: ""
    } catch (e: Exception) {
        ""
    }
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
    thread {
        try {
            val name = getFileName(context, uri) ?: "upload_${System.currentTimeMillis()}"
            val tempFile = File(context.cacheDir, name)
            context.contentResolver.openInputStream(uri)?.use { input ->
                FileOutputStream(tempFile).use { output ->
                    input.copyTo(output)
                }
            }
            val res = proxyma_bind.Proxyma_bind.uploadFile(name, tempFile.absolutePath)
            tempFile.delete()
            val err = getActionError(res)
            isRunningOnMainThread {
                if (err.isNotEmpty()) {
                    onComplete(Result.failure(Exception(err)))
                } else {
                    val msg = getActionMessage(res)
                    onComplete(Result.success(msg.ifEmpty { res }))
                }
            }
        } catch (e: Exception) {
            isRunningOnMainThread {
                onComplete(Result.failure(e))
            }
        }
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
    thread {
        try {
            val res = action()
            val err = getActionError(res)
            isRunningOnMainThread {
                onComplete?.invoke()
                if (err.isNotEmpty()) {
                    context.toast(err, long = true)
                } else {
                    onSuccess?.invoke(res)
                }
            }
        } catch (e: Exception) {
            isRunningOnMainThread {
                onComplete?.invoke()
                context.toast("Error: ${e.message}", long = true)
            }
        }
    }
}

fun executeGoSubmit(
    onComplete: (Result<String>) -> Unit,
    action: () -> String,
    onSuccess: ((String) -> Unit)? = null
) {
    thread {
        try {
            val res = action()
            val err = if (res.startsWith("error:")) {
                res.substringAfter("error:").trim()
            } else {
                getActionError(res)
            }
            isRunningOnMainThread {
                if (err.isNotEmpty()) {
                    onComplete(Result.failure(Exception(err)))
                } else {
                    onSuccess?.invoke(res)
                    onComplete(Result.success(res))
                }
            }
        } catch (e: Exception) {
            isRunningOnMainThread {
                onComplete(Result.failure(e))
            }
        }
    }
}
