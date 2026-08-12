package com.proxyma.android.ui.screens

import android.util.Log
import androidx.annotation.MainThread
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
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.FormParameter
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.models.RunDialogTarget
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.models.TaskLedger
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.PipelineEditorDialog
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.RunTaskDialog
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.components.PipelineCardItem
import com.proxyma.android.ui.components.ServiceCardItem
import com.proxyma.android.ui.components.TaskLogCardItem
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicLong
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class ServicesViewModel : ViewModel() {
    val fileTasks = mutableStateListOf<FileTask>()
    private val taskLedger = TaskLedger(fileTasks)
    private val streamJobs = ConcurrentHashMap<String, Job>()
    private val taskSequence = AtomicLong(0)

    @MainThread
    fun enqueueTask(
        name: String,
        payloadJson: String,
        streaming: Boolean,
        unaryAction: (() -> String)? = null
    ): String {
        val taskId = "task_${taskSequence.incrementAndGet()}"
        taskLedger.addFirst(
            FileTask(
                taskId = taskId,
                service = name,
                input = payloadJson,
                output = if (streaming) "stream" else "result",
                status = if (streaming) "streaming" else "running",
                isStreaming = streaming
            )
        )
        if (streaming) {
            startStreamTask(taskId, name, payloadJson)
        } else {
            startUnaryTask(
                taskId,
                unaryAction ?: { """{"error":"missing unary action"}""" }
            )
        }
        return taskId
    }

    @MainThread
    fun removeTask(task: FileTask) {
        streamJobs.remove(task.taskId)?.cancel()
        taskLedger.remove(task)
    }

    private fun startUnaryTask(taskId: String, action: () -> String) {
        viewModelScope.launch {
            val result = try {
                withContext(Dispatchers.IO) {
                    val response = bindResult(
                        action(),
                        BindMethod.LEGACY_ERROR_PREFIX
                    ).getOrThrow()
                    val reference = parseTaskResultReference(response)
                    val resultPath = reference.localPath ?: reference.blobHash?.let { hash ->
                        bindResult(
                            proxyma_bind.Proxyma_bind.getLocalBlobPath(hash),
                            BindMethod.LEGACY_ERROR_PREFIX
                        )
                            .getOrThrow()
                            .ifBlank { null }
                    }
                    response to resultPath
                }.let { Result.success(it) }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                Result.failure(error)
            }

            result.fold(
                onSuccess = { (_, resultPath) ->
                    updateTask(taskId) {
                        it.copy(status = "completed", resultPath = resultPath)
                    }
                },
                onFailure = { error ->
                    updateTask(taskId) {
                        it.copy(status = "failed", error = error.message ?: "failed")
                    }
                }
            )
        }
    }

    private fun startStreamTask(
        taskId: String,
        serviceName: String,
        payloadJson: String
    ) {
        val job = launchManagedBindStream(
            scope = viewModelScope,
            serviceName = serviceName,
            payloadJson = payloadJson,
            listenerFactory = { stop ->
                object : proxyma_bind.StreamEventListener {
                    override fun onChunk(chunkJSON: String) {
                        viewModelScope.launch {
                            updateTask(taskId) { current ->
                                val output = current.streamOutput
                                    ?.takeIf { it.isNotEmpty() }
                                    ?.plus("\n$chunkJSON")
                                    ?: chunkJSON
                                current.copy(streamOutput = output)
                            }
                        }
                    }

                    override fun onError(errMsg: String) {
                        val message = bindErrorMessage(errMsg)
                        viewModelScope.launch {
                            updateTask(taskId) {
                                it.copy(status = "failed", error = message)
                            }
                            stop()
                        }
                    }

                    override fun onComplete() {
                        viewModelScope.launch {
                            updateTask(taskId) { it.copy(status = "completed") }
                            stop()
                        }
                    }
                }
            },
            onStarted = { streamID ->
                updateTask(taskId) { it.copy(streamId = streamID) }
            },
            onStartFailure = { error ->
                updateTask(taskId) {
                    it.copy(status = "failed", error = error.message ?: "stream failed")
                }
            }
        )
        streamJobs[taskId] = job
        job.invokeOnCompletion {
            streamJobs.remove(taskId, job)
        }
    }

    @MainThread
    private fun updateTask(taskId: String, transform: (FileTask) -> FileTask) {
        taskLedger.update(taskId, transform)
    }

    override fun onCleared() {
        streamJobs.values.forEach(Job::cancel)
        streamJobs.clear()
        super.onCleared()
    }
}

@Composable
fun ServicesScreen(
    serviceDomain: Map<String, Any>?,
    taskViewModel: ServicesViewModel
) {
    val fileTasks = taskViewModel.fileTasks
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
    val scope = rememberCoroutineScope()

    var runTarget by remember { mutableStateOf(RunDialogTarget()) }

    fun triggerManualDiscovery() {
        isManualDiscovering = true
        runBindOnBg(scope, {
            Log.i("ProxymaService", "[Android UI] User triggered manual service discovery...")
            proxyma_bind.Proxyma_bind.discoverServices()
        }) { result ->
            result.fold(
                onSuccess = { raw ->
                    Log.i("ProxymaService", "[Android UI] Service discovery response: $raw")
                    val parsed = try {
                        val type = object : TypeToken<List<String>>() {}.type
                        Gson().fromJson<List<String>>(raw, type) ?: emptyList()
                    } catch (e: Exception) { emptyList() }
                    Log.i("ProxymaService", "[Android UI] Discovery scan finished. Found ${parsed.size} services: $parsed")
                    isManualDiscovering = false
                    manualServicesList = parsed
                    lastDiscoveryStatus = if (parsed.isEmpty()) "ℹ️ No services found on cluster peers." else "✅ Found ${parsed.size} active service(s)."
                    context.toast(if (parsed.isEmpty()) "ℹ️ No services found on cluster peers." else "✅ Found ${parsed.size} service(s)")
                },
                onFailure = { err ->
                    val errorMsg = err.message ?: "discovery failed"
                    Log.e("ProxymaService", "[Android UI] Service discovery error: $errorMsg")
                    isManualDiscovering = false
                    lastDiscoveryStatus = "❌ Error: $errorMsg"
                    context.toast("❌ Discovery Error: $errorMsg")
                }
            )
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
                                onOpenResult = { path, name -> openFileNatively(scope, context, path, name) }
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
                            val initialConns = pipeline.connections.filter { it.from_step == "\$initial" }
                            if (initialConns.isEmpty()) {
                                runTarget = RunDialogTarget(
                                    name = pipeline.id,
                                    isPipeline = true,
                                    specs = DEFAULT_RUN_PARAMS
                                )
                            } else {
                                runOnBg(scope, action = {
                                    val specs = initialConns.map { conn ->
                                        val fromPortName = conn.from_port
                                        val tgtStep = pipeline.steps.find { it.id == conn.to_step }
                                        val tgtSvc = tgtStep?.service ?: ""
                                        val paramDef = if (tgtSvc.isNotEmpty()) {
                                            fetchServiceDetail(tgtSvc)?.parameters?.find { it.name == conn.to_port }
                                        } else null
                                        formParameterFrom(
                                            src = paramDef,
                                            name = fromPortName
                                        )
                                    }.distinctBy { it.name }
                                    specs.ifEmpty { DEFAULT_RUN_PARAMS }
                                }) { result ->
                                    runTarget = RunDialogTarget(
                                        name = pipeline.id,
                                        isPipeline = true,
                                        specs = result.getOrDefault(DEFAULT_RUN_PARAMS)
                                    )
                                }
                            }
                        },
                        onEdit = {
                            editingPipeline = pipeline
                            showEditor = true
                        },
                        onClone = {
                            runBindOnBg(scope, {
                                proxyma_bind.Proxyma_bind.clonePipelineSchemaJson(pipeline.id, "${pipeline.id}-local", "\$local")
                            }) { result ->
                                result.fold(
                                    onSuccess = { clonedJson ->
                                        val cloned = parsePipelineSchema(clonedJson)
                                        if (cloned != null) {
                                            editingPipeline = cloned
                                            showEditor = true
                                        }
                                    },
                                    onFailure = { err ->
                                        context.toast("Error cloning pipeline: ${err.message}")
                                    }
                                )
                            }
                        },
                        onDelete = {
                            executeGoCall(
                                scope = scope,
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
                            isLoading = true
                            loadServiceDetail(scope, svcName) { detail ->
                                isLoading = false
                                serviceDetailJson = detail?.let { Gson().toJson(it) } ?: ""
                            }
                        },
                        onRun = {
                            loadRunSpecs(scope, svcName) { specs, isStream ->
                                runTarget = RunDialogTarget(
                                    name = svcName,
                                    isStreaming = isStream,
                                    specs = specs
                                )
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
                    ?: ServiceDetail(
                        description = "Failed to parse info",
                        error = "parse error"
                    )
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
                    taskViewModel = taskViewModel,
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
                    Text("Status: ${task.status.uppercase()}", color = taskStatusColor(task.status).let {
                        if (task.status == "streaming") VioletSecondary else it
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
                        taskViewModel.removeTask(task)
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

    if (runTarget.isVisible) {
        val target = runTarget
        RunTaskDialog(
            targetName = target.name.orEmpty(),
            isPipeline = target.isPipeline,
            parameterSpecs = target.specs.orEmpty(),
            onDismiss = {
                runTarget = target.reset()
            },
            onExecute = { payloadMap ->
                val payloadJson = Gson().toJson(payloadMap)
                val targetId = target.name.orEmpty()
                val isPipe = target.isPipeline
                val isStream = target.isStreaming
                runTarget = target.reset()

                context.toast(if (isStream) "🌊 Streaming $targetId..." else "🚀 Running $targetId...")
                taskViewModel.enqueueTask(
                    name = targetId,
                    payloadJson = payloadJson,
                    streaming = isStream,
                    unaryAction = {
                        if (isPipe) proxyma_bind.Proxyma_bind.runPipeline(targetId, payloadJson)
                        else proxyma_bind.Proxyma_bind.runService(targetId, payloadJson)
                    }
                )
            }
        )
    }
}
