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
import com.proxyma.android.models.UIDomain
import com.proxyma.android.ui.components.*
import com.proxyma.android.utils.*

@Composable
fun PairingScreen(service: ProxymaService?, clusterDomain: UIDomain?) {
    var generatedToken by remember { mutableStateOf("") }
    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    val title = clusterDomain?.title ?: "Pairing Controller"
    val actions = clusterDomain?.actions

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            ScreenTitle(title)
        }

        actions?.forEach { action ->
            val actionName = action.name
            val actionTitle = action.title
            val actionDescription = action.description

            item {
                if (actionName == "invite") {
                    DynamicActionCard(
                        actionName = actionName,
                        title = actionTitle,
                        description = actionDescription,
                        parameters = emptyList(),
                        submitButtonText = actionTitle,
                        onSubmit = { _, onComplete ->
                            executeGoSubmit(
                                scope = scope,
                                onComplete = onComplete,
                                action = { invokeUIAction(action) },
                                onSuccess = { response ->
                                    generatedToken = getActionMessage(response).ifBlank { response.trim('"') }
                                }
                            )
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
                    val formParams = action.parameters
                    DynamicActionCard(
                        actionName = actionName,
                        title = actionTitle,
                        description = actionDescription,
                        parameters = formParams,
                        submitButtonText = "Pair with Cluster",
                        onSubmit = { inputs, onComplete ->
                            executeGoSubmit(
                                scope = scope,
                                onComplete = onComplete,
                                action = {
                                    val storagePath = proxyma_bind.Proxyma_bind.getStoragePath()
                                    val token = inputs["token"]?.toString() ?: ""
                                    val nodeId = inputs["node_id"]?.toString() ?: ""
                                    val port = inputs["port"]?.toString()?.takeIf { it.isNotBlank() }
                                        ?: formParams.firstOrNull { it.name == "port" }?.defaultValue.orEmpty()
                                    proxyma_bind.Proxyma_bind.joinCluster(storagePath, token, nodeId, port)
                                }
                            )
                        }
                    )
                }
            }
        }
    }
}
