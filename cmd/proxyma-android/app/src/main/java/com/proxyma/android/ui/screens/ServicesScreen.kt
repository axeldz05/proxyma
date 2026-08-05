package com.proxyma.android.ui.screens

import android.net.Uri
import android.util.Log
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.PhotoCamera
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
import com.proxyma.android.models.PipelineConnection
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.models.PipelineStep
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.models.ServiceParameterSpec
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.PipelineEditorDialog
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import java.io.File
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
    var runTargetSpecs by remember { mutableStateOf<List<ServiceParameterSpec>?>(null) }

    fun triggerManualDiscovery() {
        thread {
            isManualDiscovering = true
            Log.i("ProxymaService", "[Android UI] User triggered manual service discovery...")
            val raw = proxyma_bind.Proxyma_bind.discoverServices()
            Log.i("ProxymaService", "[Android UI] Service discovery response: $raw")

            val handler = android.os.Handler(android.os.Looper.getMainLooper())
            if (raw.contains("\"error\":")) {
                val errorMsg = try {
                    val obj = Gson().fromJson(raw, Map::class.java)
                    obj["error"]?.toString() ?: raw
                } catch (e: Exception) { raw }
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
                                            val parsedDetails = try { Gson().fromJson(rawDetails, ServiceDetail::class.java) } catch (_: Exception) { null }
                                            paramDef = parsedDetails?.parameters?.find { it.name == conn.to_port }
                                        }

                                        val pType = paramDef?.type ?: "string"
                                        val pReq = paramDef?.required ?: false
                                        val pDef = paramDef?.defaultValue
                                        val pOpts = paramDef?.options
                                        val uiHint = paramDef?.uiHint

                                        val isFile = pType == "file" || uiHint == "file_picker" || uiHint == "image_picker"
                                        val isImg = uiHint == "image_picker"

                                        ServiceParameterSpec(
                                            name = fromPortName,
                                            type = pType,
                                            required = pReq,
                                            isFileInput = isFile,
                                            isImageInput = isImg,
                                            defaultValue = pDef,
                                            options = pOpts
                                        )
                                    }.distinctBy { it.name }
                                } else {
                                    listOf(
                                        ServiceParameterSpec("input_path", "string", required = true, isFileInput = true, isImageInput = true)
                                    )
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
                            if (clonedJson.contains("\"error\":")) {
                                context.toast("Error cloning pipeline: $clonedJson")
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
                                val parsedDetails = try { Gson().fromJson(rawDetails, ServiceDetail::class.java) } catch (_: Exception) { null }
                                val specs = parsedDetails?.parameters?.map { p ->
                                    val isFile = p.type == "file" || p.uiHint == "file_picker" || p.uiHint == "image_picker"
                                    val isImg = p.uiHint == "image_picker"
                                    ServiceParameterSpec(
                                        name = p.name,
                                        type = p.type,
                                        required = p.required,
                                        isFileInput = isFile,
                                        isImageInput = isImg,
                                        defaultValue = p.defaultValue,
                                        options = p.options
                                    )
                                } ?: listOf(
                                    ServiceParameterSpec("input_path", "string", required = true, isFileInput = true, isImageInput = true)
                                )
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
            val gson = remember { Gson() }
            val details: ServiceDetail = remember(serviceDetailJson) {
                try {
                    gson.fromJson(serviceDetailJson, ServiceDetail::class.java)
                } catch (e: Exception) {
                    ServiceDetail("", "Failed to parse info", false, "", emptyList(), emptyList(), null, null, e.message)
                }
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

                if (isPipe) {
                    thread {
                        val res = proxyma_bind.Proxyma_bind.runPipeline(targetId, payloadJson)
                        val err = getActionError(res)
                        isRunningOnMainThread {
                            val index = fileTasks.indexOfFirst { it.taskId == taskID }
                            if (err.isNotEmpty()) {
                                context.toast("❌ Execution failed: $err", long = true)
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(status = "failed", error = err)
                                }
                            } else {
                                context.toast("✅ Execution completed!")
                                val resPath = getResultPath(res)
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(
                                        status = "completed",
                                        resultPath = if (resPath.isNotEmpty()) resPath else null
                                    )
                                }
                            }
                        }
                    }
                } else if (isStream) {
                    proxyma_bind.Proxyma_bind.streamService(targetId, payloadJson, object : proxyma_bind.StreamEventListener {
                        override fun onChunk(chunkJSON: String) {
                            isRunningOnMainThread {
                                val index = fileTasks.indexOfFirst { it.taskId == taskID }
                                if (index != -1) {
                                    val current = fileTasks[index]
                                    val updatedOutput = if (current.streamOutput.isNullOrEmpty()) {
                                        chunkJSON
                                    } else {
                                        current.streamOutput + "\n" + chunkJSON
                                    }
                                    fileTasks[index] = current.copy(streamOutput = updatedOutput)
                                }
                            }
                        }

                        override fun onError(errMsg: String) {
                            isRunningOnMainThread {
                                val index = fileTasks.indexOfFirst { it.taskId == taskID }
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(status = "failed", error = errMsg)
                                }
                                context.toast("❌ Stream error: $errMsg", long = true)
                            }
                        }

                        override fun onComplete() {
                            isRunningOnMainThread {
                                val index = fileTasks.indexOfFirst { it.taskId == taskID }
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(status = "completed")
                                }
                                context.toast("✅ Stream completed!")
                            }
                        }
                    })
                } else {
                    thread {
                        val res = proxyma_bind.Proxyma_bind.runService(targetId, payloadJson)
                        val err = getActionError(res)
                        isRunningOnMainThread {
                            val index = fileTasks.indexOfFirst { it.taskId == taskID }
                            if (err.isNotEmpty()) {
                                context.toast("❌ Execution failed: $err", long = true)
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(status = "failed", error = err)
                                }
                            } else {
                                context.toast("✅ Execution completed!")
                                val resPath = getResultPath(res)
                                if (index != -1) {
                                    fileTasks[index] = fileTasks[index].copy(
                                        status = "completed",
                                        resultPath = if (resPath.isNotEmpty()) resPath else null
                                    )
                                }
                            }
                        }
                    }
                }
            }
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RunTaskDialog(
    targetName: String,
    isPipeline: Boolean,
    parameterSpecs: List<ServiceParameterSpec>,
    onDismiss: () -> Unit,
    onExecute: (payloadMap: Map<String, String>) -> Unit
) {
    val context = LocalContext.current
    val paramValues = remember { mutableStateMapOf<String, String>() }

    var activeParamForPicker by remember { mutableStateOf<String?>(null) }
    var activeParamForCamera by remember { mutableStateOf<String?>(null) }
    var cameraPhotoFile by remember { mutableStateOf<File?>(null) }

    val filePickerLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetContent()
    ) { uri: Uri? ->
        val paramName = activeParamForPicker
        if (uri != null && paramName != null) {
            val cachedPath = copyUriToCache(context, uri)
            paramValues[paramName] = cachedPath
            context.toast("Selected: ${cachedPath.substringAfterLast('/')}")
        }
        activeParamForPicker = null
    }

    val cameraLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.TakePicture()
    ) { success: Boolean ->
        val paramName = activeParamForCamera
        val photoFile = cameraPhotoFile
        if (success && paramName != null && photoFile != null) {
            paramValues[paramName] = photoFile.absolutePath
            context.toast("Photo captured!")
        }
        activeParamForCamera = null
        cameraPhotoFile = null
    }

    LaunchedEffect(parameterSpecs) {
        parameterSpecs.forEach { spec ->
            if (!spec.defaultValue.isNullOrEmpty() && paramValues[spec.name].isNullOrEmpty()) {
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
                        val currentValue = paramValues[spec.name] ?: ""
                        ServiceParameterField(
                            spec = spec,
                            currentValue = currentValue,
                            onValueChange = { paramValues[spec.name] = it },
                            onPickFile = {
                                activeParamForPicker = spec.name
                                filePickerLauncher.launch("*/*")
                            },
                            onTakePhoto = {
                                val (uri, file) = createTempCameraFile(context)
                                cameraPhotoFile = file
                                activeParamForCamera = spec.name
                                cameraLauncher.launch(uri)
                            }
                        )
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    val missing = parameterSpecs.filter { it.required && (paramValues[it.name]?.trim() ?: "").isEmpty() }
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
fun ServiceParameterField(
    spec: ServiceParameterSpec,
    currentValue: String,
    onValueChange: (String) -> Unit,
    onPickFile: () -> Unit,
    onTakePhoto: () -> Unit
) {
    Column(modifier = Modifier.fillMaxWidth()) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(
                text = "${spec.name}${if (spec.required) " *" else " (opt)"}",
                fontWeight = FontWeight.Bold,
                color = if (spec.required) Color.White else Color.LightGray,
                fontSize = 14.sp
            )
            Text("type: ${spec.type}", fontSize = 11.sp, color = Color.Gray)
        }
        Spacer(Modifier.height(4.dp))

        if (!spec.options.isNullOrEmpty()) {
            var expandedOptions by remember { mutableStateOf(false) }
            Box(modifier = Modifier.fillMaxWidth()) {
                OutlinedButton(
                    onClick = { expandedOptions = true },
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(if (currentValue.isEmpty()) "Select Preset..." else "Preset: $currentValue", fontSize = 12.sp, color = Color.White)
                }
                DropdownMenu(
                    expanded = expandedOptions,
                    onDismissRequest = { expandedOptions = false }
                ) {
                    spec.options.forEach { opt ->
                        DropdownMenuItem(
                            text = { Text(opt) },
                            onClick = {
                                onValueChange(opt)
                                expandedOptions = false
                            }
                        )
                    }
                }
            }
            Spacer(Modifier.height(4.dp))
        }

        OutlinedTextField(
            value = currentValue,
            onValueChange = onValueChange,
            label = { Text("Value / Path for ${spec.name}") },
            modifier = Modifier.fillMaxWidth(),
            singleLine = true
        )
        Spacer(Modifier.height(4.dp))
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.End
        ) {
            if (spec.isFileInput) {
                IconButton(onClick = onPickFile) {
                    Icon(Icons.Default.Folder, contentDescription = "Pick File", tint = MintGreen)
                }
            }
            if (spec.isImageInput) {
                IconButton(onClick = onTakePhoto) {
                    Icon(Icons.Default.PhotoCamera, contentDescription = "Take Photo", tint = VioletSecondary)
                }
            }
        }
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

