package com.proxyma.android.ui.screens

import android.content.Context
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.ProxymaService
import com.proxyma.android.ui.components.*
import com.proxyma.android.utils.*
import kotlin.concurrent.thread

@Suppress("UNCHECKED_CAST")
@Composable
fun PairingScreen(service: ProxymaService?, clusterDomain: Map<String, Any>?) {
    var generatedToken by remember { mutableStateOf("") }
    val context = LocalContext.current

    val title = (clusterDomain?.get("title") as? String) ?: "Pairing Controller"
    val actions = clusterDomain?.get("actions") as? List<Map<String, Any>>

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Text(
                text = title,
                fontSize = 24.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )
        }

        actions?.forEach { action ->
            val actionName = action["name"] as? String
            val actionTitle = (action["title"] as? String) ?: ""
            val actionDescription = action["description"] as? String
            val parameters = action["parameters"] as? List<Map<String, Any>>

            item {
                if (actionName == "invite") {
                    DynamicActionCard(
                        actionName = actionName,
                        title = actionTitle,
                        description = actionDescription,
                        parameters = emptyList(),
                        submitButtonText = actionTitle,
                        onSubmit = { _, onComplete ->
                            executeGoSubmit(onComplete, {
                                proxyma_bind.Proxyma_bind.generateInviteToken()
                            }) { token ->
                                generatedToken = token
                            }
                        }
                    )

                    if (generatedToken.isNotEmpty()) {
                        Spacer(Modifier.height(12.dp))
                        OutlinedTextField(
                            value = generatedToken,
                            onValueChange = {},
                            readOnly = true,
                            label = { Text("Smart Invite Token") },
                            modifier = Modifier.fillMaxWidth(),
                            trailingIcon = {
                                IconButton(onClick = {
                                    val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
                                    val clip = android.content.ClipData.newPlainText("proxyma_token", generatedToken)
                                    clipboard.setPrimaryClip(clip)
                                    context.toast("Token copied!")
                                }) {
                                    Icon(Icons.Default.ContentCopy, contentDescription = "Copy")
                                }
                            }
                        )
                    }
                } else if (actionName == "join") {
                    val formParams = (parameters ?: emptyList()).map { param ->
                        FormParameter(
                            name = (param["name"] as? String) ?: "",
                            type = (param["type"] as? String) ?: "string",
                            required = (param["required"] as? Boolean) ?: false,
                            description = (param["description"] as? String) ?: "",
                            uiHint = param["uiHint"] as? String,
                            defaultValue = param["defaultValue"] as? String
                        )
                    }
                    DynamicActionCard(
                        actionName = actionName,
                        title = actionTitle,
                        description = actionDescription,
                        parameters = formParams,
                        submitButtonText = "Pair with Cluster",
                        onSubmit = { inputs, onComplete ->
                            executeGoSubmit(onComplete, {
                                val storagePath = proxyma_bind.Proxyma_bind.getStoragePath()
                                val token = inputs["token"]?.toString() ?: ""
                                val nodeId = inputs["node_id"]?.toString() ?: ""
                                val port = inputs["port"]?.toString() ?: "8080"
                                proxyma_bind.Proxyma_bind.joinCluster(storagePath, token, nodeId, port)
                            })
                        }
                    )
                }
            }
        }
    }
}
