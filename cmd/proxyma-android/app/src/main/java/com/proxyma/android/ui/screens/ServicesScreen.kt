package com.proxyma.android.ui.screens

import android.util.Log
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.CloudQueue
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.FormParameter
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.ParameterInput
import com.proxyma.android.ui.components.PipelineEditorDialog
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import kotlin.concurrent.thread

private val fileTasks = mutableStateListOf<FileTask>()

@Composable
fun ServicesScreen(serviceDomain: Map<String, Any>?) {
    val serviceNames by rememberPolledParsedState(4000, emptyList<String>()) {
        proxyma_bind.Proxyma_bind.discoverServices()
    }
    val pipelinesList by rememberPolledParsedState(2000, emptyList<PipelineSchema>()) {
        proxyma_bind.Proxyma_bind.listPipelines()
    }
    var selectedService by remember { mutableStateOf<String?>(null) }
    var serviceDetailJson by remember { mutableStateOf("") }
    var isLoading by remember { mutableStateOf(false) }
    var activeDetailTask by remember { mutableStateOf<FileTask?>(null) }
    var showEditor by remember { mutableStateOf(false) }
    var editingPipeline by remember { mutableStateOf<PipelineSchema?>(null) }
    var isManualDiscovering by remember { mutableStateOf(false) }
    var lastDiscoveryStatus by remember { mutableStateOf<String?>(null) }
    var manualServicesList by remember { mutableStateOf<List<String>?>(null) }
    val displayedServices = manualServicesList ?: serviceNames
    val context = LocalContext.current

    var runTargetName by remember { mutableStateOf<String?>(null) }
    var runTargetIsPipeline by remember { mutableStateOf(false) }
    var runTargetIsStreaming by remember { mutableStateOf(false) }
    var runTargetSpecs by remember { mutableStateOf<List<FormParameter>?>(null) }

    fun triggerManualDiscovery() {
        thread {
            isManualDiscovering = true
            Log.i("ProxymaService", "[Android UI] User triggered manual service discovery...")
            val raw = proxyma_bind.Proxyma_bind.discoverServices()
            Log.i("ProxymaService", "[Android UI] Service discovery response: $raw")

            val handler = android.os.Handler(android.os.Looper.getMainLooper())
            if (isBindError(raw)) {
                val errorMsg = parseBindError(raw).ifEmpty { raw }
                Log.e("ProxymaService", "[Android UI] Service discovery error: $errorMsg")
                handler.post {
                    isManualDiscovering = false
                    lastDiscoveryStatus = "❌ Error: $errorMsg"
                    context.toast("❌ Discovery Error: $errorMsg")
                }
            } else {
                val parsed = try {
                    val type = object : TypeToken<List<String>>() {}.type
                    Gson().fromJson<List<String>>(raw, type) ?: emptyList()
                } catch (e: Exception) { emptyList() }
                Log.i("ProxymaService", "[Android UI] Discovery scan finished. Found ${parsed.size} services: $parsed")
                handler.post {
                    isManualDiscovering = false
                    manualServicesList = parsed
                    lastDiscoveryStatus = if (parsed.isEmpty()) "ℹ️ No services found on cluster peers." else "✅ Found ${parsed.size} active service(s)."
                    context.toast(if (parsed.isEmpty()) "ℹ️ No services found on cluster peers." else "✅ Found ${parsed.size} service(s)")
                }
            }
        }
    }

    if (selectedService == null) {
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            item {
                ScreenTitle((serviceDomain?.get("title") as? String) ?: "Cluster Services")
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
                            TaskLogCardItem(
                                task = task,
                                onClick = { activeDetailTask = task },
                                onOpenResult = { path, name -> openFileNatively(context, path, name) }
                            )
                        }
                    }
                }
            }

            // 2. Pipelines Section
            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "Pipelines",
                        fontSize = 18.sp,
                        fontWeight = FontWeight.Bold,
                        color = Color.White
                    )
                    IconButton(
                        onClick = {
                            editingPipeline = PipelineSchema("", 1, emptyList(), emptyList())
                            showEditor = true
                        }
                    ) {
                        Icon(Icons.Default.Add, contentDescription = "Create Pipeline", tint = MintGreen)
                    }
                }
            }

            if (pipelinesList.isEmpty()) {
                item {
                    Text("No pipelines defined yet.", color = Color.Gray)
                }
            } else {
                items(pipelinesList, key = { it.id }) { pipeline ->
                    PipelineCardItem(
                        pipeline = pipeline,
                        onRun = {
                            thread {
                                val initialConns = pipeline.connections.filter { it.from_step == "\$initial" }
                                val specs = if (initialConns.isNotEmpty()) {
                                    initialConns.map { conn ->
                                        val fromPortName = conn.from_port
                                        val tgtStep = pipeline.steps.find { it.id == conn.to_step }
                                        val tgtSvc = tgtStep?.service ?: ""

                                        var paramDef: FormParameter? = null
                                        if (tgtSvc.isNotEmpty()) {
                                            val rawDetails = proxyma_bind.Proxyma_bind.getServiceDetails(tgtSvc)
                                            paramDef = parseServiceDetail(rawDetails)?.parameters?.find { it.name == conn.to_port }
                                        }

                                        FormParameter(
                                            name = fromPortName,
                                            type = paramDef?.type ?: "string",
                                            required = paramDef?.required ?: false,
                                            description = paramDef?.description ?: "",
                                            uiHint = paramDef?.uiHint,
                                            defaultValue = paramDef?.defaultValue,
                                            options = paramDef?.options
                                        )
                                    }.distinctBy { it.name }
                                } else {
                                    DEFAULT_RUN_PARAMS
                                }

                                isRunningOnMainThread {
                                    runTargetName = pipeline.id
                                    runTargetIsPipeline = true
                                    runTargetSpecs = specs
                                }
                            }
                        },
                        onEdit = {
                            editingPipeline = pipeline
                            showEditor = true
                        },
                        onClone = {
                            val clonedJson = proxyma_bind.Proxyma_bind.clonePipelineSchemaJson(pipeline.id, "${pipeline.id}-local", "\$local")
                            if (isBindError(clonedJson)) {
                                context.toast("Error cloning pipeline: ${parseBindError(clonedJson).ifEmpty { clonedJson }}")
                            } else {
                                val cloned = parsePipelineSchema(clonedJson)
                                if (cloned != null) {
                                    editingPipeline = cloned
                                    showEditor = true
                                }
                            }
                        },
                        onDelete = {
                            executeGoCall(
                                context = context,
                                onStart = {},
                                onComplete = {},
                                action = { proxyma_bind.Proxyma_bind.removePipeline(pipeline.id) }
                            ) {
                                context.toast("Pipeline '${pipeline.id}' removed successfully.")
                            }
                        }
                    )
                }
            }

            item { Spacer(Modifier.height(16.dp)) }

            // 3. Discovered services list
            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "Discovered Services",
                        fontSize = 18.sp,
                        fontWeight = FontWeight.Bold,
                        color = Color.White
                    )
                    IconButton(
                        onClick = { triggerManualDiscovery() },
                        enabled = !isManualDiscovering
                    ) {
                        if (isManualDiscovering) {
                            CircularProgressIndicator(modifier = Modifier.size(18.dp), color = MintGreen)
                        } else {
                            Icon(Icons.Default.Refresh, contentDescription = "Refresh Services", tint = MintGreen)
                        }
                    }
                }
                if (lastDiscoveryStatus != null) {
                    Spacer(Modifier.height(4.dp))
                    Text(
                        text = lastDiscoveryStatus!!,
                        fontSize = 12.sp,
                        color = if (lastDiscoveryStatus!!.startsWith("❌")) Color.Red else MintGreen
                    )
                }
            }

            if (displayedServices.isEmpty()) {
                item {
                    Text("No other compute services discovered.", color = Color.Gray)
                }
            } else {
                items(displayedServices, key = { it }) { svcName ->
                    ServiceCardItem(
                        svcName = svcName,
                        onClick = {
                            selectedService = svcName
                            executeGoCall(
                                context = context,
                                onStart = { isLoading = true },
                                onComplete = { isLoading = false },
                                action = { proxyma_bind.Proxyma_bind.getServiceDetails(svcName) }
                            ) { details ->
                                serviceDetailJson = details
                            }
                        },
                        onRun = {
                            thread {
                                val rawDetails = proxyma_bind.Proxyma_bind.getServiceDetails(svcName)
                                val parsedDetails = parseServiceDetail(rawDetails)
                                val specs = parsedDetails?.parameters?.takeIf { it.isNotEmpty() } ?: DEFAULT_RUN_PARAMS
                                val isStream = parsedDetails?.isStreaming == true
                                isRunningOnMainThread {
                                    runTargetName = svcName
                                    runTargetIsPipeline = false
                                    runTargetIsStreaming = isStream
                                    runTargetSpecs = specs
                                }
                            }
                        }
                    )
                }
            }
        }
    } else {
        if (isLoading) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(color = VioletPrimary)
            }
        } else {
            val details: ServiceDetail = remember(serviceDetailJson) {
                parseServiceDetail(serviceDetailJson)
                    ?: ServiceDetail("", "Failed to parse info", false, "", emptyList(), emptyList(), null, null, "parse error")
            }

            if (details.ui != null && details.ui.type == "web_app") {
                ServiceWebContainerScreen(
                    serviceName = details.name,
                    uiConfig = details.ui,
                    onBack = {
                        selectedService = null
                        serviceDetailJson = ""
                    }
                )
            } else {
                ServiceDetailLayout(
                    details = details,
                    fileTasks = fileTasks,
                    onBack = {
                        selectedService = null
                        serviceDetailJson = ""
                    }
                )
            }
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
                        "streaming" -> VioletSecondary
                        else -> Color.Yellow
                    }, fontWeight = FontWeight.Bold, fontSize = 14.sp)
                    Text("Input:\n${task.input}", color = Color.White, fontSize = 14.sp)
                    Text("Output Target:\n${task.output}", color = Color.White, fontSize = 14.sp)
                    if (task.streamOutput != null) {
                        Text("Streaming Output Chunks:", color = MintGreen, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                        Box(
                            modifier = Modifier
                                .fillMaxWidth()
                                .heightIn(max = 200.dp)
                                .background(Color.Black, shape = RoundedCornerShape(6.dp))
                                .padding(8.dp)
                                .verticalScroll(rememberScrollState())
                        ) {
                            Text(task.streamOutput, color = MintGreen, fontSize = 12.sp, fontFamily = FontFamily.Monospace)
                        }
                    }
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

    if (showEditor) {
        PipelineEditorDialog(
            pipeline = editingPipeline,
            services = serviceNames,
            onDismiss = { showEditor = false }
        )
    }

    if (runTargetName != null && runTargetSpecs != null) {
        RunTaskDialog(
            targetName = runTargetName!!,
            isPipeline = runTargetIsPipeline,
            parameterSpecs = runTargetSpecs!!,
            onDismiss = {
                runTargetName = null
                runTargetSpecs = null
            },
            onExecute = { payloadMap ->
                val payloadJson = Gson().toJson(payloadMap)
                val targetId = runTargetName!!
                val isPipe = runTargetIsPipeline
                val isStream = runTargetIsStreaming
                runTargetName = null
                runTargetSpecs = null
                runTargetIsStreaming = false

                val taskID = "task_${System.currentTimeMillis()}"
                val newTask = FileTask(
                    taskId = taskID,
                    service = targetId,
                    input = payloadJson,
                    output = if (isStream) "stream" else "result",
                    status = if (isStream) "streaming" else "running",
                    isStreaming = isStream
                )
                fileTasks.add(0, newTask)
                context.toast(if (isStream) "🌊 Streaming $targetId..." else "🚀 Running $targetId...")

                if (isStream) {
                    attachStreamToFileTask(
                        fileTasks = fileTasks,
                        taskId = taskID,
                        serviceName = targetId,
                        payloadJson = payloadJson,
                        context = context
                    )
                } else {
                    startUnaryFileTask(
                        fileTasks = fileTasks,
                        taskId = taskID,
                        context = context,
                        action = {
                            if (isPipe) proxyma_bind.Proxyma_bind.runPipeline(targetId, payloadJson)
                            else proxyma_bind.Proxyma_bind.runService(targetId, payloadJson)
                        }
                    )
                }
            }
        )
    }
}

@Composable
fun RunTaskDialog(
    targetName: String,
    isPipeline: Boolean,
    parameterSpecs: List<FormParameter>,
    onDismiss: () -> Unit,
    onExecute: (payloadMap: Map<String, Any>) -> Unit
) {
    val context = LocalContext.current
    val paramValues = remember { mutableStateMapOf<String, Any>() }

    LaunchedEffect(parameterSpecs) {
        parameterSpecs.forEach { spec ->
            if (!spec.defaultValue.isNullOrEmpty() && paramValues[spec.name] == null) {
                paramValues[spec.name] = spec.defaultValue
            }
        }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Run ${if (isPipeline) "Pipeline" else "Service"}: $targetName") },
        text = {
            LazyColumn(
                modifier = Modifier.fillMaxWidth().heightIn(max = 400.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                if (parameterSpecs.isEmpty()) {
                    item {
                        Text("No specific input parameters required.", color = Color.Gray, fontSize = 14.sp)
                    }
                } else {
                    items(parameterSpecs) { spec ->
                        ParameterInput(
                            param = spec,
                            value = paramValues[spec.name],
                            onValueChange = { paramValues[spec.name] = it },
                            localFilePath = true,
                            enableCamera = true
                        )
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    val missing = parameterSpecs.filter {
                        it.required && (paramValues[it.name]?.toString()?.trim() ?: "").isEmpty()
                    }
                    if (missing.isNotEmpty()) {
                        context.toast("Missing required field(s): ${missing.joinToString { it.name }}")
                        return@Button
                    }
                    onExecute(paramValues.toMap())
                }
            ) {
                Text("Execute")
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text("Cancel")
            }
        }
    )
}

private fun parsePipelineSchema(json: String): PipelineSchema? {
    return try {
        Gson().fromJson(json, PipelineSchema::class.java)
    } catch (e: Exception) {
        null
    }
}

@Composable
fun TaskLogCardItem(
    task: FileTask,
    onClick: () -> Unit,
    onOpenResult: (path: String, outputName: String) -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier
            .width(260.dp)
            .clickable(onClick = onClick)
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
                    onClick = { onOpenResult(task.resultPath, task.output) },
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

@Composable
fun PipelineCardItem(
    pipeline: PipelineSchema,
    onRun: () -> Unit,
    onEdit: () -> Unit,
    onClone: () -> Unit,
    onDelete: () -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column {
                    Text(pipeline.id, fontWeight = FontWeight.Bold, color = Color.White, fontSize = 16.sp)
                    Text("Version: ${pipeline.version} | Steps: ${pipeline.steps.size}", fontSize = 12.sp, color = Color.Gray)
                }

                Row {
                    IconButton(onClick = onRun) {
                        Icon(Icons.Default.PlayArrow, contentDescription = "Run Pipeline", tint = MintGreen)
                    }
                    IconButton(onClick = onEdit) {
                        Icon(Icons.Default.Edit, contentDescription = "Edit Pipeline", tint = VioletSecondary)
                    }
                    IconButton(onClick = onClone) {
                        Icon(Icons.Default.ContentCopy, contentDescription = "Clone & Localize Pipeline", tint = MintGreen)
                    }
                    IconButton(onClick = onDelete) {
                        Icon(Icons.Default.Delete, contentDescription = "Delete Pipeline", tint = Color.Red)
                    }
                }
            }
        }
    }
}

@Composable
fun ServiceCardItem(
    svcName: String,
    onClick: () -> Unit,
    onRun: () -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick)
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
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onRun) {
                    Icon(Icons.Default.PlayArrow, contentDescription = "Run Service", tint = MintGreen)
                }
                Icon(Icons.Default.ChevronRight, contentDescription = "Open Details", tint = Color.Gray)
            }
        }
    }
}

