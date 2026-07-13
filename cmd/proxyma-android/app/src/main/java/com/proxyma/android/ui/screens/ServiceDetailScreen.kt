package com.proxyma.android.ui.screens

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
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
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.DynamicActionForm
import com.proxyma.android.ui.components.FormParameter
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import kotlin.concurrent.thread

@Composable
fun ServiceDetailLayout(
    details: ServiceDetail,
    fileTasks: MutableList<FileTask>,
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val parametersList = details.parameters ?: emptyList()
    val isFileService = parametersList.any { it.type == "file" }

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onBack) {
                    Icon(Icons.Default.ArrowBack, contentDescription = "Back", tint = Color.White)
                }
                Spacer(Modifier.width(8.dp))
                Text(details.name, fontSize = 24.sp, fontWeight = FontWeight.Bold, color = Color.White)
            }
        }

        item {
            ProxymaCard {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Description", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(Modifier.height(4.dp))
                    Text(details.description ?: "No description provided.", color = Color.White)
                    Spacer(Modifier.height(8.dp))
                    Text("Provider Address", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(Modifier.height(4.dp))
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
                        Spacer(Modifier.height(6.dp))
                        permissions.forEach { perm ->
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.Check, contentDescription = "Check", tint = MintGreen, modifier = Modifier.size(16.dp))
                                Spacer(Modifier.width(8.dp))
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

        val formParams = mutableListOf<FormParameter>()
        
        if (isFileService) {
            if (parametersList.none { it.name.lowercase() == "input" }) {
                formParams.add(FormParameter(
                    name = "input",
                    type = "file",
                    required = true,
                    description = "Input file to process (local path or VFS)",
                    uiHint = "file_picker"
                ))
            }
            if (parametersList.none { it.name.lowercase() == "output" }) {
                formParams.add(FormParameter(
                    name = "output",
                    type = "string",
                    required = false,
                    description = "Output VFS name (optional, defaults to output_input)",
                    uiHint = null
                ))
            }
        }

        parametersList.sortedByDescending { it.required }.forEach { param ->
            val lowerName = param.name.lowercase()
            if (isFileService && (lowerName == "input" || lowerName == "output")) {
                // Already added custom input/output picker above
            } else {
                val uiHint = if (param.type == "file") {
                    if (lowerName.contains("image") || lowerName.contains("img") || lowerName.contains("photo")) {
                        "image_picker"
                    } else {
                        "file_picker"
                    }
                } else {
                    param.uiHint
                }
                formParams.add(param.copy(uiHint = uiHint))
            }
        }

        if (formParams.isEmpty()) {
            item {
                Text("This service requires no parameters.", color = Color.Gray)
            }
        } else {
            item {
                DynamicActionForm(
                    parameters = formParams,
                    submitButtonText = if (isFileService) "Submit File Task" else "Execute Task",
                    onSubmit = { inputs, onComplete ->
                        if (isFileService) {
                            val input = inputs["input"]?.toString() ?: ""
                            val output = inputs["output"]?.toString() ?: ""
                            val otherParams = inputs.filterKeys { it.lowercase() != "input" && it.lowercase() != "output" }
                            val paramJson = if (otherParams.isNotEmpty()) {
                                Gson().toJson(otherParams)
                            } else {
                                ""
                            }

                            val taskId = "task_ui_${System.currentTimeMillis()}"
                            val newTask = FileTask(
                                taskId = taskId,
                                service = details.name,
                                input = input,
                                output = output,
                                status = "running"
                            )
                            fileTasks.add(0, newTask)

                            // Go back to the main Services view immediately so progress is visible
                            onBack()

                            thread {
                                android.util.Log.d("ProxymaUI", "Starting runFileService for: " + details.name)
                                val resultJson = proxyma_bind.Proxyma_bind.runFileService(details.name, input, output, paramJson)
                                android.util.Log.d("ProxymaUI", "runFileService returned: " + resultJson)
                                isRunningOnMainThread {
                                    val map = try {
                                        Gson().fromJson<Map<String, Any>>(resultJson, object : TypeToken<Map<String, Any>>() {}.type)
                                    } catch (e: Exception) {
                                        android.util.Log.e("ProxymaUI", "JSON parse error: " + e.message)
                                        mapOf("error" to "Daemon response error: ${e.message}")
                                    }

                                    val idx = fileTasks.indexOfFirst { it.taskId == taskId }
                                    if (idx != -1) {
                                        val errVal = map["error"] as? String
                                        if (errVal != null) {
                                            fileTasks[idx] = fileTasks[idx].copy(
                                                status = "failed",
                                                error = errVal
                                            )
                                            onComplete(Result.failure(Exception(errVal)))
                                        } else {
                                            val outputsMap = map["outputs"] as? Map<*, *>
                                            val hash = outputsMap?.get("output_hash") as? String
                                            val finalPath = if (hash != null) {
                                                proxyma_bind.Proxyma_bind.getLocalBlobPath(hash)
                                            } else {
                                                null
                                            }
                                            fileTasks[idx] = fileTasks[idx].copy(
                                                status = "completed",
                                                resultPath = finalPath
                                            )
                                            onComplete(Result.success("Task completed successfully"))
                                        }
                                    } else {
                                        onComplete(Result.success("Task completed"))
                                    }
                                }
                            }
                        } else {
                            val payloadJson = Gson().toJson(inputs)
                            executeGoSubmit(onComplete, {
                                proxyma_bind.Proxyma_bind.runService(details.name, payloadJson)
                            })
                        }
                    }
                )
            }
        }
    }
}
