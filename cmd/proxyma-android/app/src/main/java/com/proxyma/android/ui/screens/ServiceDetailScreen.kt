package com.proxyma.android.ui.screens

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Check
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.gson.Gson
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.DynamicActionForm
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*

@Composable
fun ServiceDetailLayout(
    details: ServiceDetail,
    fileTasks: MutableList<FileTask>,
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val formParams = (details.parameters ?: emptyList()).sortedByDescending { it.required }

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onBack) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = Color.White)
                }
                Spacer(modifier.width(8.dp))
                Text(details.name, fontSize = 24.sp, fontWeight = FontWeight.Bold, color = Color.White)
            }
        }

        item {
            ProxymaCard {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Description", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(modifier.height(4.dp))
                    Text(details.description ?: "No description provided.", color = Color.White)
                    Spacer(modifier.height(8.dp))
                    Text("Provider Address", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(modifier.height(4.dp))
                    Text(details.providerAddress ?: "Unknown", color = Color.White, fontSize = 13.sp)
                }
            }
        }

        val permissions = details.requiredPermissions ?: emptyList()
        if (permissions.isNotEmpty()) {
            item {
                ProxymaCard {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text("Required Permissions", fontWeight = FontWeight.Bold, color = Color.Gray)
                        Spacer(modifier.height(6.dp))
                        permissions.forEach { perm ->
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.Check, contentDescription = "Check", tint = MintGreen, modifier = Modifier.size(16.dp))
                                Spacer(modifier.width(8.dp))
                                Text(perm, color = Color.White)
                            }
                        }
                    }
                }
            }
        }

        item {
            Text("Parameters", fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
        }

        if (formParams.isEmpty()) {
            item {
                Text("This service requires no parameters.", color = Color.Gray)
            }
        }
        item {
            DynamicActionForm(
                parameters = formParams,
                submitButtonText = if (details.isStreaming == true) "Start Stream" else "Execute Task",
                onSubmit = { inputs, onComplete ->
                    val payloadJson = Gson().toJson(inputs)
                    val streaming = details.isStreaming == true
                    enqueueFileTask(
                        fileTasks = fileTasks,
                        name = details.name,
                        payloadJson = payloadJson,
                        streaming = streaming,
                        context = context,
                        unaryAction = { proxyma_bind.Proxyma_bind.runService(details.name, payloadJson) },
                        onDone = onComplete
                    )
                    onBack()
                }
            )
        }
    }
}
