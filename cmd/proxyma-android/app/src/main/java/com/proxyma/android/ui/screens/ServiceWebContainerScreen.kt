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
import com.proxyma.android.utils.isRunningOnMainThread

class ProxymaJSBridge(private val webView: WebView) {

    @JavascriptInterface
    fun runService(name: String, payloadJson: String): String {
        return try {
            proxyma_bind.Proxyma_bind.runService(name, payloadJson)
        } catch (e: Exception) {
            "{\"error\": \"${e.localizedMessage}\"}"
        }
    }

    @JavascriptInterface
    fun streamService(name: String, payloadJson: String, callbackName: String) {
        try {
            proxyma_bind.Proxyma_bind.streamService(name, payloadJson, object : proxyma_bind.StreamEventListener {
                override fun onChunk(chunkJSON: String) {
                    isRunningOnMainThread {
                        val escaped = chunkJSON.replace("'", "\\'").replace("\n", "\\n")
                        webView.evaluateJavascript("if (typeof window['$callbackName'] === 'function') window['$callbackName']('$escaped');", null)
                    }
                }

                override fun onError(errMsg: String) {
                    android.util.Log.e("ProxymaJSBridge", "Stream error for $name: $errMsg")
                    isRunningOnMainThread {
                        val escaped = errMsg.replace("'", "\\'").replace("\n", "\\n")
                        webView.evaluateJavascript("if (typeof window['${callbackName}_error'] === 'function') window['${callbackName}_error']('$escaped');", null)
                    }
                }

                override fun onComplete() {
                    isRunningOnMainThread {
                        webView.evaluateJavascript("if (typeof window['${callbackName}_complete'] === 'function') window['${callbackName}_complete']();", null)
                    }
                }
            })
        } catch (e: Exception) {
            val errStr = e.localizedMessage ?: "Stream error"
            android.util.Log.e("ProxymaJSBridge", "Stream exception for $name: $errStr", e)
            isRunningOnMainThread {
                val escaped = errStr.replace("'", "\\'").replace("\n", "\\n")
                webView.evaluateJavascript("if (typeof window['${callbackName}_error'] === 'function') window['${callbackName}_error']('$escaped');", null)
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ServiceWebContainerScreen(
    serviceName: String,
    uiConfig: ServiceUIConfig,
    onBack: () -> Unit
) {
    val htmlContent = remember(serviceName) {
        proxyma_bind.Proxyma_bind.getServiceUIContent(serviceName)
    }

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
            if (htmlContent.isBlank() && uiConfig.url.isNullOrBlank()) {
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
                            addJavascriptInterface(ProxymaJSBridge(this), "ProxymaBridge")

                            if (!uiConfig.url.isNullOrBlank()) {
                                loadUrl(uiConfig.url)
                            } else {
                                loadDataWithBaseURL("file:///android_asset/", htmlContent, "text/html", "UTF-8", null)
                            }
                        }
                    },
                    modifier = Modifier.fillMaxSize()
                )
            }
        }
    }
}
