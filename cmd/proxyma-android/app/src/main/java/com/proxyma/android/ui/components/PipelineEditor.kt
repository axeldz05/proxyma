package com.proxyma.android.ui.components

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
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
import com.proxyma.android.models.Peer
import com.proxyma.android.models.PipelineConnection
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.models.PipelineStep
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.executeGoCall
import com.proxyma.android.utils.isBindError
import com.proxyma.android.utils.loadServiceDetailsMap
import com.proxyma.android.utils.parseBindError
import com.proxyma.android.utils.runBindOnBg
import com.proxyma.android.utils.toast

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PipelineEditorDialog(
    pipeline: PipelineSchema?,
    services: List<String>,
    onDismiss: () -> Unit
) {
    var pipelineId by remember { mutableStateOf(pipeline?.id ?: "") }
    var versionStr by remember { mutableStateOf(pipeline?.version?.toString() ?: "1") }

    val steps = remember { mutableStateListOf<PipelineStep>().apply { pipeline?.steps?.let { addAll(it) } } }
    val connections = remember { mutableStateListOf<PipelineConnection>().apply { pipeline?.connections?.let { addAll(it) } } }

    var serviceDetails by remember { mutableStateOf<Map<String, ServiceDetail>>(emptyMap()) }
    var knownPeers by remember { mutableStateOf<List<Peer>>(emptyList()) }
    var localNodeId by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()

    LaunchedEffect(steps.map { it.service }) {
        val uniqueServices = (services + steps.map { it.service }).distinct()
        loadServiceDetailsMap(scope, uniqueServices) { map ->
            serviceDetails = map
        }
        runBindOnBg(scope, {
            val node = proxyma_bind.Proxyma_bind.getNodeID()
            val rawPeers = proxyma_bind.Proxyma_bind.getPeersJson()
            Gson().toJson(mapOf("node" to node, "peers" to rawPeers))
        }) { result ->
            result.onSuccess { json ->
                try {
                    val root = Gson().fromJson<Map<String, Any>>(json, object : TypeToken<Map<String, Any>>() {}.type)
                    val node = root["node"] as? String ?: ""
                    if (node.isNotEmpty()) {
                        localNodeId = node
                    }
                    val rawPeers = root["peers"] as? String ?: ""
                    if (!isBindError(rawPeers)) {
                        val listType = object : TypeToken<List<Peer>>() {}.type
                        val peersList = Gson().fromJson<List<Peer>>(rawPeers, listType)
                        if (peersList != null) {
                            knownPeers = peersList
                        }
                    }
                } catch (_: Exception) {}
            }
        }
    }

    var showAddStep by remember { mutableStateOf(false) }
    var showAddConnection by remember { mutableStateOf(false) }
    val context = LocalContext.current

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (pipeline?.id?.isEmpty() == true) "Create Pipeline" else "Edit Pipeline") },
        text = {
            LazyColumn(
                modifier = Modifier.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                item {
                    OutlinedTextField(
                        value = pipelineId,
                        onValueChange = { pipelineId = it },
                        label = { Text("Pipeline ID") },
                        modifier = Modifier.fillMaxWidth(),
                        enabled = pipeline?.id?.isEmpty() == true
                    )
                }

                item {
                    OutlinedTextField(
                        value = versionStr,
                        onValueChange = { versionStr = it },
                        label = { Text("Version") },
                        modifier = Modifier.fillMaxWidth()
                    )
                }

                // Steps section
                item {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Text("Steps", fontWeight = FontWeight.Bold, fontSize = 16.sp, color = Color.White)
                        IconButton(onClick = { showAddStep = true }) {
                            Icon(Icons.Default.Add, contentDescription = "Add Step", tint = MintGreen)
                        }
                    }
                }

                if (steps.isEmpty()) {
                    item { Text("No steps added.", color = Color.Gray, fontSize = 12.sp) }
                } else {
                    items(steps) { step ->
                        PipelineStepCard(
                            step = step,
                            details = serviceDetails[step.service],
                            connections = connections,
                            onDelete = {
                                steps.remove(step)
                                connections.removeAll { it.from_step == step.id || it.to_step == step.id }
                            }
                        )
                    }
                }

                // Connections section
                item {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Text("Connections", fontWeight = FontWeight.Bold, fontSize = 16.sp, color = Color.White)
                        IconButton(onClick = { showAddConnection = true }) {
                            Icon(Icons.Default.Add, contentDescription = "Add Connection", tint = MintGreen)
                        }
                    }
                }

                if (connections.isEmpty()) {
                    item { Text("No connections defined.", color = Color.Gray, fontSize = 12.sp) }
                } else {
                    items(connections) { conn ->
                        PipelineConnectionCard(
                            conn = conn,
                            onDelete = { connections.remove(conn) }
                        )
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    if (pipelineId.trim().isEmpty()) {
                        context.toast("Pipeline ID is required")
                        return@Button
                    }
                    val ver = versionStr.toIntOrNull() ?: 1
                    val newSchema = PipelineSchema(
                        id = pipelineId.trim(),
                        version = ver,
                        steps = steps.toList(),
                        connections = connections.toList()
                    )
                    val jsonStr = Gson().toJson(newSchema)

                    executeGoCall(
                        scope = scope,
                        context = context,
                        onStart = {},
                        onComplete = {},
                        action = { proxyma_bind.Proxyma_bind.addPipelineRaw(pipelineId.trim(), jsonStr) }
                    ) { res ->
                        if (isBindError(res)) {
                            context.toast("Validation failed: ${parseBindError(res).ifEmpty { res }}", long = true)
                        } else {
                            context.toast("Pipeline saved successfully!")
                            onDismiss()
                        }
                    }
                }
            ) {
                Text("Save")
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text("Cancel")
            }
        }
    )

    if (showAddStep) {
        AddStepDialog(
            services = services,
            localNodeId = localNodeId,
            knownPeers = knownPeers,
            onDismiss = { showAddStep = false },
            onAdd = { newStep ->
                steps.add(newStep)
                showAddStep = false
            }
        )
    }

    if (showAddConnection) {
        AddConnectionDialog(
            steps = steps,
            serviceDetails = serviceDetails,
            connections = connections,
            onDismiss = { showAddConnection = false },
            onAdd = { newConn ->
                connections.add(newConn)
                showAddConnection = false
            }
        )
    }
}

@Composable
fun PipelineStepCard(
    step: PipelineStep,
    details: ServiceDetail?,
    connections: List<PipelineConnection>,
    onDelete: () -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = DeepGray),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier.padding(12.dp).fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(step.id, fontWeight = FontWeight.Bold, color = Color.White)
                Text("Service: ${step.service}", fontSize = 12.sp, color = Color.LightGray)
                if (!step.target_node_id.isNullOrEmpty()) {
                    Text("Node: ${step.target_node_id}", fontSize = 12.sp, color = Color.Gray)
                }
                if (details != null) {
                    val paramsList = details.parameters
                    if (!paramsList.isNullOrEmpty()) {
                        val inPortsStr = paramsList.joinToString(", ") { p ->
                            val reqTag = if (p.required) "req" else "opt"
                            val connFrom = connections.find { it.to_step == step.id && it.to_port == p.name }?.from_step
                            if (connFrom != null) "${p.name} ($reqTag, ← $connFrom)" else "${p.name} ($reqTag)"
                        }
                        Text("Inputs: $inPortsStr", fontSize = 11.sp, color = MintGreen)
                    }
                    val outsMap = details.outputs
                    if (!outsMap.isNullOrEmpty()) {
                        val outPortsStr = outsMap.keys.joinToString(", ") { outKey ->
                            val connTo = connections.find { it.from_step == step.id && it.from_port == outKey }?.to_step
                            if (connTo != null) "$outKey (→ $connTo)" else outKey
                        }
                        Text("Outputs: $outPortsStr", fontSize = 11.sp, color = VioletSecondary)
                    }
                }
            }
            IconButton(onClick = onDelete) {
                Icon(Icons.Default.Delete, contentDescription = "Delete", tint = Color.Red)
            }
        }
    }
}

@Composable
fun PipelineConnectionCard(
    conn: PipelineConnection,
    onDelete: () -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = DeepGray),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier.padding(12.dp).fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = "[${conn.from_step}].${conn.from_port}",
                    fontSize = 13.sp,
                    color = VioletSecondary
                )
                Text(
                    text = " ──► [${conn.to_step}].${conn.to_port}",
                    fontSize = 13.sp,
                    color = MintGreen
                )
            }
            IconButton(onClick = onDelete) {
                Icon(Icons.Default.Delete, contentDescription = "Delete", tint = Color.Red)
            }
        }
    }
}

@Composable
fun AddStepDialog(
    services: List<String>,
    localNodeId: String,
    knownPeers: List<Peer>,
    onDismiss: () -> Unit,
    onAdd: (step: PipelineStep) -> Unit
) {
    var stepIdInput by remember { mutableStateOf("") }
    var selectedSvcIdx by remember { mutableStateOf(0) }
    var expandedSvc by remember { mutableStateOf(false) }

    var expandedNodeDropdown by remember { mutableStateOf(false) }
    var selectedNodeValue by remember { mutableStateOf("") }
    var selectedNodeLabel by remember { mutableStateOf("Any / Auto (Cluster Bidding)") }
    var isCustomNodeInput by remember { mutableStateOf(false) }
    var customNodeText by remember { mutableStateOf("") }
    val context = LocalContext.current

    val nodeOptions = remember(localNodeId, knownPeers) {
        val list = mutableListOf<Pair<String, String>>()
        list.add(Pair("", "Any / Auto (Cluster Bidding)"))
        list.add(Pair("\$local", "Local Node (\$local${if (localNodeId.isNotEmpty()) " - $localNodeId" else ""})"))
        if (localNodeId.isNotEmpty()) {
            list.add(Pair(localNodeId, "Local ID ($localNodeId)"))
        }
        for (p in knownPeers) {
            if (p.id != localNodeId) {
                val statusTag = if (p.online) "Online" else "Offline"
                list.add(Pair(p.id, "Peer: ${p.id} ($statusTag)"))
            }
        }
        list
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Add Pipeline Step") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(
                    value = stepIdInput,
                    onValueChange = { stepIdInput = it },
                    label = { Text("Step ID (e.g. step_ocr)") },
                    modifier = Modifier.fillMaxWidth()
                )

                Text("Service", fontWeight = FontWeight.Bold)
                if (services.isNotEmpty()) {
                    Box {
                        OutlinedButton(
                            onClick = { expandedSvc = true },
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            Text(services.getOrElse(selectedSvcIdx) { "Select Service" })
                        }
                        DropdownMenu(
                            expanded = expandedSvc,
                            onDismissRequest = { expandedSvc = false }
                        ) {
                            services.forEachIndexed { i, svc ->
                                DropdownMenuItem(
                                    text = { Text(svc) },
                                    onClick = {
                                        selectedSvcIdx = i
                                        expandedSvc = false
                                    }
                                )
                            }
                        }
                    }
                } else {
                    Text("No registered services found.", color = Color.Red)
                }

                Text("Target Node Assignment", fontWeight = FontWeight.Bold)
                if (!isCustomNodeInput) {
                    Box {
                        OutlinedButton(
                            onClick = { expandedNodeDropdown = true },
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            Text(selectedNodeLabel, fontSize = 12.sp)
                        }
                        DropdownMenu(
                            expanded = expandedNodeDropdown,
                            onDismissRequest = { expandedNodeDropdown = false }
                        ) {
                            nodeOptions.forEach { (nodeVal, label) ->
                                DropdownMenuItem(
                                    text = { Text(label) },
                                    onClick = {
                                        selectedNodeValue = nodeVal
                                        selectedNodeLabel = label
                                        expandedNodeDropdown = false
                                    }
                                )
                            }
                            DropdownMenuItem(
                                text = { Text("+ Enter Custom Node ID...", color = MintGreen) },
                                onClick = {
                                    isCustomNodeInput = true
                                    expandedNodeDropdown = false
                                }
                            )
                        }
                    }
                } else {
                    OutlinedTextField(
                        value = customNodeText,
                        onValueChange = { customNodeText = it },
                        label = { Text("Custom Target Node ID") },
                        modifier = Modifier.fillMaxWidth()
                    )
                    TextButton(onClick = { isCustomNodeInput = false }) {
                        Text("← Select from list", fontSize = 12.sp, color = MintGreen)
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    val stepName = stepIdInput.trim()
                    if (stepName.isEmpty()) {
                        context.toast("Step ID cannot be empty")
                        return@Button
                    }
                    if (services.isEmpty()) {
                        context.toast("No service selected")
                        return@Button
                    }
                    val chosenNode = if (isCustomNodeInput) customNodeText.trim() else selectedNodeValue
                    onAdd(
                        PipelineStep(
                            id = stepName,
                            service = services[selectedSvcIdx],
                            target_node_id = chosenNode.ifEmpty { null }
                        )
                    )
                }
            ) {
                Text("Add")
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text("Cancel")
            }
        }
    )
}

@Composable
fun AddConnectionDialog(
    steps: List<PipelineStep>,
    serviceDetails: Map<String, ServiceDetail>,
    connections: List<PipelineConnection>,
    onDismiss: () -> Unit,
    onAdd: (connection: PipelineConnection) -> Unit
) {
    val context = LocalContext.current
    val stepsOptions = remember { listOf("\$initial") + steps.map { it.id } }
    var selectedSrcIdx by remember { mutableStateOf(0) }
    var expandedSrc by remember { mutableStateOf(false) }

    val targetStepsOptions = remember { steps.map { it.id } }
    var selectedTgtIdx by remember { mutableStateOf(0) }
    var expandedTgt by remember { mutableStateOf(false) }

    var srcPortSelected by remember { mutableStateOf("") }
    var isCustomSrcPort by remember { mutableStateOf(false) }
    var expandedSrcPort by remember { mutableStateOf(false) }

    var tgtPortSelected by remember { mutableStateOf("") }
    var isCustomTgtPort by remember { mutableStateOf(false) }
    var expandedTgtPort by remember { mutableStateOf(false) }

    // Source step outputs context
    val currentFromStepId = stepsOptions.getOrElse(selectedSrcIdx) { "\$initial" }
    val fromStepObj: PipelineStep? = steps.find { it.id == currentFromStepId }
    val fromServiceDetail: ServiceDetail? = fromStepObj?.let { serviceDetails[it.service] }

    val availableSrcOutputs: List<Pair<String, String>> = remember(currentFromStepId, fromServiceDetail, connections.toList()) {
        if (currentFromStepId == "\$initial") {
            // Ports come only from existing $initial connections — never invent service-specific names.
            connections.filter { it.from_step == "\$initial" }.map { it.from_port }.distinct()
                .map { Pair(it, "initial input") }
        } else {
            val outs = fromServiceDetail?.outputs
            if (outs != null) {
                outs.entries.map { Pair(it.key, it.value.type) }
            } else {
                emptyList()
            }
        }
    }

    // Target step inputs (parameters) context
    val currentToStepId = targetStepsOptions.getOrElse(selectedTgtIdx) { "" }
    val toStepObj: PipelineStep? = steps.find { it.id == currentToStepId }
    val toServiceDetail: ServiceDetail? = toStepObj?.let { serviceDetails[it.service] }

    val availableTgtInputs: List<Triple<String, String, Boolean>> = remember(currentToStepId, toServiceDetail) {
        val params = toServiceDetail?.parameters
        if (params != null) {
            params.map { Triple(it.name, it.type, it.required) }
        } else {
            emptyList()
        }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Add Connection") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                // 1. Source Step Selection
                Text("Source (Outputs)", fontWeight = FontWeight.Bold, color = VioletSecondary)
                Box {
                    OutlinedButton(
                        onClick = { expandedSrc = true },
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text("From Step: $currentFromStepId")
                    }
                    DropdownMenu(
                        expanded = expandedSrc,
                        onDismissRequest = { expandedSrc = false }
                    ) {
                        stepsOptions.forEachIndexed { i, opt ->
                            DropdownMenuItem(
                                text = { Text(opt) },
                                onClick = {
                                    selectedSrcIdx = i
                                    expandedSrc = false
                                    srcPortSelected = ""
                                    isCustomSrcPort = false
                                }
                            )
                        }
                    }
                }

                // Source Port Selection (Context: OUTPUTS)
                if (availableSrcOutputs.isNotEmpty() && !isCustomSrcPort) {
                    Box {
                        OutlinedButton(
                            onClick = { expandedSrcPort = true },
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            val labelText = if (srcPortSelected.isEmpty()) "Select Output Port..." else "Output Port: $srcPortSelected"
                            Text(labelText, color = if (srcPortSelected.isEmpty()) Color.Gray else Color.White)
                        }
                        DropdownMenu(
                            expanded = expandedSrcPort,
                            onDismissRequest = { expandedSrcPort = false }
                        ) {
                            availableSrcOutputs.forEach { (portName, portType) ->
                                val connectedToStep = connections.find { it.from_step == currentFromStepId && it.from_port == portName }?.to_step
                                val isUsed = connectedToStep != null
                                val connTag = if (isUsed) " (→ $connectedToStep)" else ""
                                val itemText = "$portName (type: $portType)$connTag"

                                DropdownMenuItem(
                                    text = {
                                        Text(
                                            text = itemText,
                                            color = if (isUsed) Color.Gray else Color.White
                                        )
                                    },
                                    onClick = {
                                        srcPortSelected = portName
                                        expandedSrcPort = false
                                    }
                                )
                            }
                            DropdownMenuItem(
                                text = { Text("+ Enter Custom Port Name...", color = MintGreen) },
                                onClick = {
                                    isCustomSrcPort = true
                                    srcPortSelected = ""
                                    expandedSrcPort = false
                                }
                            )
                        }
                    }
                } else {
                    OutlinedTextField(
                        value = srcPortSelected,
                        onValueChange = { srcPortSelected = it },
                        label = { Text(if (currentFromStepId == "\$initial") "Initial Output Port Name" else "Output Port Name (Custom)") },
                        modifier = Modifier.fillMaxWidth()
                    )
                    if (availableSrcOutputs.isNotEmpty()) {
                        TextButton(onClick = { isCustomSrcPort = false; srcPortSelected = "" }) {
                            Text("← Pick from Service Outputs list", fontSize = 11.sp, color = MintGreen)
                        }
                    }
                }

                Spacer(Modifier.height(8.dp))

                // 2. Target Step Selection
                Text("Target (Inputs)", fontWeight = FontWeight.Bold, color = MintGreen)
                if (targetStepsOptions.isNotEmpty()) {
                    Box {
                        OutlinedButton(
                            onClick = { expandedTgt = true },
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            Text("To Step: $currentToStepId")
                        }
                        DropdownMenu(
                            expanded = expandedTgt,
                            onDismissRequest = { expandedTgt = false }
                        ) {
                            targetStepsOptions.forEachIndexed { i, opt ->
                                DropdownMenuItem(
                                    text = { Text(opt) },
                                    onClick = {
                                        selectedTgtIdx = i
                                        expandedTgt = false
                                        tgtPortSelected = ""
                                        isCustomTgtPort = false
                                    }
                                )
                            }
                        }
                    }
                } else {
                    Text("No target steps available.", color = Color.Red)
                }

                // Target Port Selection (Context: INPUTS)
                if (availableTgtInputs.isNotEmpty() && !isCustomTgtPort) {
                    Box {
                        OutlinedButton(
                            onClick = { expandedTgtPort = true },
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            val labelText = if (tgtPortSelected.isEmpty()) "Select Input Port..." else "Input Port: $tgtPortSelected"
                            Text(labelText, color = if (tgtPortSelected.isEmpty()) Color.Gray else Color.White)
                        }
                        DropdownMenu(
                            expanded = expandedTgtPort,
                            onDismissRequest = { expandedTgtPort = false }
                        ) {
                            availableTgtInputs.forEach { (portName, portType, isReq) ->
                                val connectedFromStep = connections.find { it.to_step == currentToStepId && it.to_port == portName }?.from_step
                                val isUsed = connectedFromStep != null
                                val connTag = if (isUsed) " (← $connectedFromStep)" else ""
                                val reqTag = if (isReq) "req" else "opt"
                                val itemText = "$portName ($reqTag, type: $portType)$connTag"

                                DropdownMenuItem(
                                    text = {
                                        Text(
                                            text = itemText,
                                            color = if (isUsed) Color.Gray else if (isReq) Color.White else Color.LightGray
                                        )
                                    },
                                    onClick = {
                                        tgtPortSelected = portName
                                        expandedTgtPort = false
                                    }
                                )
                            }
                            DropdownMenuItem(
                                text = { Text("+ Enter Custom Port Name...", color = MintGreen) },
                                onClick = {
                                    isCustomTgtPort = true
                                    tgtPortSelected = ""
                                    expandedTgtPort = false
                                }
                            )
                        }
                    }
                } else {
                    OutlinedTextField(
                        value = tgtPortSelected,
                        onValueChange = { tgtPortSelected = it },
                        label = { Text("Input Port Name (Custom)") },
                        modifier = Modifier.fillMaxWidth()
                    )
                    if (availableTgtInputs.isNotEmpty()) {
                        TextButton(onClick = { isCustomTgtPort = false; tgtPortSelected = "" }) {
                            Text("← Pick from Service Inputs list", fontSize = 11.sp, color = MintGreen)
                        }
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    val fromP = srcPortSelected.trim()
                    val toP = tgtPortSelected.trim()
                    if (fromP.isEmpty() || toP.isEmpty()) {
                        context.toast("Both source and target ports must be defined")
                        return@Button
                    }
                    if (currentFromStepId == currentToStepId) {
                        context.toast("Self-loop connections are not allowed")
                        return@Button
                    }
                    onAdd(
                        PipelineConnection(
                            from_step = currentFromStepId,
                            from_port = fromP,
                            to_step = currentToStepId,
                            to_port = toP
                        )
                    )
                }
            ) {
                Text("Add Connection")
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text("Cancel")
            }
        }
    )
}
