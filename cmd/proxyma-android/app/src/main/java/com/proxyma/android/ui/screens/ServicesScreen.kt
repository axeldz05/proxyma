package com.proxyma.android.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
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
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*

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
                            ProxymaCard(
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
                    ProxymaCard(
                        shape = RoundedCornerShape(10.dp),
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable {
                                selectedService = svcName
                                executeGoCall(
                                    context = context,
                                    onStart = { isLoading = true },
                                    onComplete = { isLoading = false },
                                    action = { proxyma_bind.Proxyma_bind.getServiceDetails(svcName) }
                                ) { details ->
                                    serviceDetailJson = details
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
                fileTasks = fileTasks,
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

