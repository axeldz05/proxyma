package com.proxyma.android.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.CloudQueue
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
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.DynamicActionForm
import com.proxyma.android.ui.components.FormParameter
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import kotlin.concurrent.thread

data class FileTask(
    val taskId: String,
    val service: String,
    val input: String,
    val output: String,
    val status: String, // "running", "completed", "failed"
    val resultPath: String? = null,
    val error: String? = null
)

private val fileTasks = mutableStateListOf<FileTask>()

@Composable
fun ServicesScreen(serviceDomain: Map<String, Any>?) {
    val serviceNames by rememberPolledParsedState(4000, emptyList<String>()) {
        proxyma_bind.Proxyma_bind.discoverServices()
    }
    var selectedService by remember { mutableStateOf<String?>(null) }
    var serviceDetailJson by remember { mutableStateOf("") }
    var isLoading by remember { mutableStateOf(false) }
    var activeDetailTask by remember { mutableStateOf<FileTask?>(null) }
    val context = LocalContext.current

    if (selectedService == null) {
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            item {
                Text(
                    text = (serviceDomain?.get("title") as? String) ?: "Cluster Services",
                    fontSize = 24.sp,
                    fontWeight = FontWeight.Bold,
                    color = Color.White
                )
            }

            // 1. Horizontal list of file task executions
            if (fileTasks.isNotEmpty()) {
                item {
                    Text(
                        text = "File Task Logs",
                        fontSize = 18.sp,
                        fontWeight = FontWeight.Bold,
                        color = Color.White
                    )
                    Spacer(Modifier.height(8.dp))
                    LazyRow(
                        horizontalArrangement = Arrangement.spacedBy(10.dp),
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        items(fileTasks) { task ->
                            Card(
                                colors = CardDefaults.cardColors(containerColor = CardGray),
                                shape = RoundedCornerShape(10.dp),
                                modifier = Modifier
                                    .width(260.dp)
                                    .clickable { activeDetailTask = task }
                            ) {
                                Column(modifier = Modifier.padding(12.dp)) {
                                    Row(
                                        modifier = Modifier.fillMaxWidth(),
                                        horizontalArrangement = Arrangement.SpaceBetween,
                                        verticalAlignment = Alignment.CenterVertically
                                    ) {
                                        Text(
                                            text = task.service.uppercase(),
                                            fontWeight = FontWeight.Bold,
                                            fontSize = 14.sp,
                                            color = VioletSecondary
                                        )
                                        val statusColor = when (task.status) {
                                            "completed" -> MintGreen
                                            "failed" -> Color.Red
                                            else -> Color.Yellow
                                        }
                                        Text(
                                            text = task.status.uppercase(),
                                            fontWeight = FontWeight.Bold,
                                            fontSize = 11.sp,
                                            color = statusColor
                                        )
                                    }
                                    Spacer(Modifier.height(6.dp))
                                    Text("Input: ${task.input}", fontSize = 12.sp, color = Color.LightGray, maxLines = 1)
                                    Text("Output: ${task.output}", fontSize = 12.sp, color = Color.LightGray, maxLines = 1)
                                    if (task.status == "completed" && task.resultPath != null) {
                                        Spacer(Modifier.height(8.dp))
                                        Button(
                                            onClick = {
                                                openFileNatively(context, task.resultPath, task.output)
                                            },
                                            colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                                            modifier = Modifier
                                                .fillMaxWidth()
                                                .height(32.dp),
                                            contentPadding = PaddingValues(0.dp)
                                        ) {
                                            Text("Open Result", fontSize = 12.sp, color = Color.White)
                                        }
                                    } else if (task.status == "failed" && task.error != null) {
                                        Spacer(Modifier.height(6.dp))
                                        Text(task.error, fontSize = 11.sp, color = Color.Red, maxLines = 2)
                                    }
                                }
                            }
                        }
                    }
                }
            }



            // 3. Discovered services list
            item {
                Text(
                    text = "Discovered Services",
                    fontSize = 18.sp,
                    fontWeight = FontWeight.Bold,
                    color = Color.White
                )
            }

            if (serviceNames.isEmpty()) {
                item {
                    Text("No other compute services discovered.", color = Color.Gray)
                }
            } else {
                items(serviceNames) { svcName ->
                    Card(
                        colors = CardDefaults.cardColors(containerColor = CardGray),
                        shape = RoundedCornerShape(10.dp),
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable {
                                selectedService = svcName
                                isLoading = true
                                thread {
                                    val details = proxyma_bind.Proxyma_bind.getServiceDetails(svcName)
                                    isRunningOnMainThread {
                                        serviceDetailJson = details
                                        isLoading = false
                                    }
                                }
                            }
                    ) {
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(16.dp),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.CloudQueue, contentDescription = "Compute", tint = VioletSecondary)
                                Spacer(Modifier.width(12.dp))
                                Text(svcName, fontWeight = FontWeight.Bold, color = Color.White)
                            }
                            Icon(Icons.Default.ChevronRight, contentDescription = "Open Details", tint = Color.Gray)
                        }
                    }
                }
            }
        }
    } else {
        if (isLoading) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(color = VioletPrimary)
            }
        } else {
            val gson = remember { Gson() }
            val details: ServiceDetail = remember(serviceDetailJson) {
                try {
                    gson.fromJson(serviceDetailJson, ServiceDetail::class.java)
                } catch (e: Exception) {
                    ServiceDetail("", "Failed to parse info", "", emptyList(), emptyList(), e.message)
                }
            }

            ServiceDetailLayout(
                details = details,
                onBack = {
                    selectedService = null
                    serviceDetailJson = ""
                }
            )
        }
    }

    if (activeDetailTask != null) {
        val task = activeDetailTask!!
        AlertDialog(
            onDismissRequest = { activeDetailTask = null },
            title = {
                Text(
                    text = "${task.service.uppercase()} Task Details",
                    fontWeight = FontWeight.Bold,
                    color = Color.White
                )
            },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text("Task ID:\n${task.taskId}", color = Color.Gray, fontSize = 12.sp)
                    Text("Status: ${task.status.uppercase()}", color = when (task.status) {
                        "completed" -> MintGreen
                        "failed" -> Color.Red
                        else -> Color.Yellow
                    }, fontWeight = FontWeight.Bold, fontSize = 14.sp)
                    Text("Input File:\n${task.input}", color = Color.White, fontSize = 14.sp)
                    Text("Output VFS Name:\n${task.output}", color = Color.White, fontSize = 14.sp)
                    if (task.resultPath != null) {
                        Text("Result Path:\n${task.resultPath}", color = MintGreen, fontSize = 13.sp)
                    }
                    if (task.error != null) {
                        Text("Error:\n${task.error}", color = Color.Red, fontSize = 13.sp)
                    }
                }
            },
            confirmButton = {
                TextButton(
                    onClick = {
                        fileTasks.remove(task)
                        activeDetailTask = null
                    }
                ) {
                    Text("Delete", color = Color.Red, fontWeight = FontWeight.Bold)
                }
            },
            dismissButton = {
                TextButton(onClick = { activeDetailTask = null }) {
                    Text("Close", color = Color.White)
                }
            },
            containerColor = CardGray,
            textContentColor = Color.White
        )
    }
}

@Composable
fun ServiceDetailLayout(details: ServiceDetail, onBack: () -> Unit) {
    val nameLower = details.name.lowercase()
    val descLower = (details.description ?: "").lowercase()
    val hasFileKeywords = nameLower.contains("ocr") || nameLower.contains("file") || nameLower.contains("image") || nameLower.contains("pdf") || nameLower.contains("photo") || nameLower.contains("convert") || nameLower.contains("compress") ||
                          descLower.contains("ocr") || descLower.contains("file") || descLower.contains("image") || descLower.contains("pdf") || descLower.contains("photo") || descLower.contains("document")

    val parametersList = details.parameters ?: emptyList()
    val hasFileParams = parametersList.any { 
        val paramLower = it.name.lowercase()
        paramLower.contains("file") || paramLower.contains("path") || paramLower.contains("image") || paramLower.contains("img") || paramLower.contains("photo")
    }

    val isFileService = hasFileKeywords || hasFileParams

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
            Card(
                colors = CardDefaults.cardColors(containerColor = CardGray),
                shape = RoundedCornerShape(12.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
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
                Card(
                    colors = CardDefaults.cardColors(containerColor = CardGray),
                    shape = RoundedCornerShape(12.dp),
                    modifier = Modifier.fillMaxWidth()
                ) {
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
                    type = "string",
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
                val uiHint = if (lowerName.contains("image") || lowerName.contains("img") || lowerName.contains("photo")) {
                    "image_picker"
                } else if (lowerName.contains("file") || lowerName.contains("path")) {
                    "file_picker"
                } else {
                    null
                }
                formParams.add(FormParameter(
                    name = param.name,
                    type = param.type,
                    required = param.required,
                    description = param.description ?: "",
                    uiHint = uiHint
                ))
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
