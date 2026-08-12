package com.proxyma.android.ui.screens

import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.layout.*
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import com.proxyma.android.models.ServiceUIConfig
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.ProxymaCard
import com.google.gson.Gson
import com.proxyma.android.utils.BindMethod
import com.proxyma.android.utils.bindResult
import com.proxyma.android.utils.bindErrorMessage
import com.proxyma.android.utils.launchManagedBindStream
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import org.json.JSONObject

class ProxymaJSBridge(private val webView: WebView) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val streamJobs = ConcurrentHashMap<String, Job>()

    @JavascriptInterface
    fun runService(name: String, payloadJson: String): String {
        return runBlocking(Dispatchers.IO) {
            try {
                val response = proxyma_bind.Proxyma_bind.runService(name, payloadJson)
                bindResult(response, BindMethod.LEGACY_ERROR_PREFIX).fold(
                    onSuccess = { it },
                    onFailure = { Gson().toJson(mapOf("error" to (it.message ?: "service failed"))) }
                )
            } catch (error: Exception) {
                Gson().toJson(mapOf("error" to (error.localizedMessage ?: "service failed")))
            }
        }
    }

    @JavascriptInterface
    fun streamService(name: String, payloadJson: String, callbackName: String) {
        val job = launchManagedBindStream(
            scope = scope,
            serviceName = name,
            payloadJson = payloadJson,
            listenerFactory = { stop ->
                object : proxyma_bind.StreamEventListener {
                    override fun onChunk(chunkJSON: String) {
                        scope.launch(Dispatchers.Main.immediate) {
                            webView.evaluateJavascript(
                                "if (typeof window[${jsString(callbackName)}] === 'function') " +
                                    "window[${jsString(callbackName)}](${jsString(chunkJSON)});",
                                null
                            )
                        }
                    }

                    override fun onError(errMsg: String) {
                        val message = bindErrorMessage(errMsg)
                        android.util.Log.e("ProxymaJSBridge", "Stream error for $name: $message")
                        scope.launch(Dispatchers.Main.immediate) {
                            val errorCallback = "${callbackName}_error"
                            webView.evaluateJavascript(
                                "if (typeof window[${jsString(errorCallback)}] === 'function') " +
                                    "window[${jsString(errorCallback)}](${jsString(message)});",
                                null
                            )
                            stop()
                        }
                    }

                    override fun onComplete() {
                        scope.launch(Dispatchers.Main.immediate) {
                            val completeCallback = "${callbackName}_complete"
                            webView.evaluateJavascript(
                                "if (typeof window[${jsString(completeCallback)}] === 'function') " +
                                    "window[${jsString(completeCallback)}]();",
                                null
                            )
                            stop()
                        }
                    }
                }
            },
            onStartFailure = { error ->
                val message = error.message ?: "Stream error"
                android.util.Log.e("ProxymaJSBridge", "Stream exception for $name: $message", error)
                scope.launch(Dispatchers.Main.immediate) {
                    val errorCallback = "${callbackName}_error"
                    webView.evaluateJavascript(
                        "if (typeof window[${jsString(errorCallback)}] === 'function') " +
                            "window[${jsString(errorCallback)}](${jsString(message)});",
                        null
                    )
                }
            }
        )
        streamJobs.put(callbackName, job)?.cancel()
        job.invokeOnCompletion {
            streamJobs.remove(callbackName, job)
        }
    }

    fun close() {
        streamJobs.values.forEach { it.cancel() }
        streamJobs.clear()
        scope.cancel()
    }

    private fun jsString(value: String): String = JSONObject.quote(value)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ServiceWebContainerScreen(
    serviceName: String,
    uiConfig: ServiceUIConfig,
    onBack: () -> Unit
) {
    val contentResult by produceState<Result<String>?>(initialValue = null, serviceName) {
        value = try {
            withContext(Dispatchers.IO) {
                bindResult(proxyma_bind.Proxyma_bind.getServiceUIContent(serviceName))
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            Result.failure(error)
        }
    }
    val htmlContent = contentResult?.getOrNull().orEmpty()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(12.dp)
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(bottom = 8.dp)
        ) {
            IconButton(onClick = onBack) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = Color.White)
            }
            Spacer(Modifier.width(8.dp))
            Column {
                Text(serviceName, fontSize = 20.sp, fontWeight = FontWeight.Bold, color = Color.White)
                Text(
                    "Dynamic Service UI (${uiConfig.widget_type ?: uiConfig.type ?: "delegated"})",
                    fontSize = 12.sp,
                    color = Color.LightGray
                )
            }
        }

        ProxymaCard(modifier = Modifier.fillMaxSize()) {
            if (contentResult == null) {
                Box(
                    modifier = Modifier.fillMaxSize(),
                    contentAlignment = Alignment.Center
                ) {
                    CircularProgressIndicator()
                }
            } else if (contentResult?.isFailure == true ||
                (htmlContent.isBlank() && uiConfig.url.isNullOrBlank())
            ) {
                Box(
                    modifier = Modifier.fillMaxSize(),
                    contentAlignment = Alignment.Center
                ) {
                    Text(
                        "Unable to load service UI content for '$serviceName'.",
                        color = Color.Red,
                        fontSize = 14.sp
                    )
                }
            } else {
                AndroidView(
                    factory = { ctx ->
                        WebView(ctx).apply {
                            settings.javaScriptEnabled = true
                            settings.domStorageEnabled = true
                            webViewClient = WebViewClient()
                            val bridge = ProxymaJSBridge(this)
                            tag = bridge
                            addJavascriptInterface(bridge, "ProxymaBridge")

                            if (!uiConfig.url.isNullOrBlank()) {
                                loadUrl(uiConfig.url)
                            } else {
                                loadDataWithBaseURL("file:///android_asset/", htmlContent, "text/html", "UTF-8", null)
                            }
                        }
                    },
                    modifier = Modifier.fillMaxSize(),
                    onRelease = { webView ->
                        (webView.tag as? ProxymaJSBridge)?.close()
                        webView.removeJavascriptInterface("ProxymaBridge")
                        webView.stopLoading()
                        webView.destroy()
                    }
                )
            }
        }
    }
}
