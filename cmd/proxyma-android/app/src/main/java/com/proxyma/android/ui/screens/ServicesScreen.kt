package com.proxyma.android.ui.screens

import android.net.Uri
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.ParameterDetail
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.getFileName
import com.proxyma.android.utils.isRunningOnMainThread
import java.io.File
import java.io.FileOutputStream
import kotlin.concurrent.fixedRateTimer
import kotlin.concurrent.thread

@Composable
fun ServicesScreen() {
    var servicesJson by remember { mutableStateOf("[]") }
    var selectedService by remember { mutableStateOf<String?>(null) }
    var serviceDetailJson by remember { mutableStateOf("") }
    var isLoading by remember { mutableStateOf(false) }

    DisposableEffect(Unit) {
        val timer = fixedRateTimer(period = 4000) {
            try {
                if (proxyma_bind.Proxyma_bind.isNodeRunning()) {
                    servicesJson = proxyma_bind.Proxyma_bind.discoverServices()
                }
            } catch (e: Exception) {
                e.printStackTrace()
            }
        }
        onDispose {
            timer.cancel()
        }
    }

    val gson = remember { Gson() }
    val serviceNames: List<String> = remember(servicesJson) {
        try {
            gson.fromJson<List<String>>(servicesJson, object : TypeToken<List<String>>() {}.type) ?: emptyList()
        } catch (e: Exception) {
            emptyList()
        }
    }

    if (selectedService == null) {
        Column(modifier = Modifier
            .fillMaxSize()
            .padding(16.dp)) {
            Text(
                text = "Cluster Services",
                fontSize = 24.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )
            Spacer(Modifier.height(16.dp))

            if (serviceNames.isEmpty()) {
                Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Text("No compute services discovered.", color = Color.Gray)
                }
            } else {
                LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp)) {
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
        }
    } else {
        if (isLoading) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(color = VioletPrimary)
            }
        } else {
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
}

@Composable
fun ServiceDetailLayout(details: ServiceDetail, onBack: () -> Unit) {
    val context = LocalContext.current
    var isRunningTask by remember { mutableStateOf(false) }

    val inputs = remember { mutableStateMapOf<String, Any>() }

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
                    Text(details.description, color = Color.White)
                    Spacer(Modifier.height(8.dp))
                    Text("Provider Address", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(Modifier.height(4.dp))
                    Text(details.providerAddress, color = Color.White, fontSize = 13.sp)
                }
            }
        }

        if (details.requiredPermissions.isNotEmpty()) {
            item {
                Card(
                    colors = CardDefaults.cardColors(containerColor = CardGray),
                    shape = RoundedCornerShape(12.dp),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text("Required Permissions", fontWeight = FontWeight.Bold, color = Color.Gray)
                        Spacer(Modifier.height(6.dp))
                        details.requiredPermissions.forEach { perm ->
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

        if (details.parameters.isEmpty()) {
            item {
                Text("This service requires no parameters.", color = Color.Gray)
            }
        } else {
            items(details.parameters) { param ->
                ParameterInputRow(param = param, onValueChange = { inputs[param.name] = it })
            }
        }

        item {
            Button(
                onClick = {
                    isRunningTask = true
                    thread {
                        try {
                            val payloadJson = Gson().toJson(inputs)
                            val resp = proxyma_bind.Proxyma_bind.runService(details.name, payloadJson)
                            isRunningOnMainThread {
                                isRunningTask = false
                                Toast.makeText(context, "Complete: $resp", Toast.LENGTH_LONG).show()
                            }
                        } catch (e: Exception) {
                            isRunningOnMainThread {
                                isRunningTask = false
                                Toast.makeText(context, "Execution failed: ${e.message}", Toast.LENGTH_LONG).show()
                            }
                        }
                    }
                },
                colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                modifier = Modifier.fillMaxWidth(),
                enabled = !isRunningTask
            ) {
                if (isRunningTask) {
                    CircularProgressIndicator(color = Color.White, modifier = Modifier.size(24.dp))
                } else {
                    Text("Execute Task", fontWeight = FontWeight.Bold)
                }
            }
        }
    }
}

@Composable
fun ParameterInputRow(param: ParameterDetail, onValueChange: (Any) -> Unit) {
    var textVal by remember { mutableStateOf("") }
    var boolVal by remember { mutableStateOf(false) }

    val isImageParam = remember(param.name) {
        val lower = param.name.lowercase()
        lower.contains("image") || lower.contains("img") || lower.contains("photo")
    }

    val context = LocalContext.current

    val imagePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetContent()
    ) { uri: Uri? ->
        if (uri != null) {
            thread {
                val name = getFileName(context, uri) ?: "photo_${System.currentTimeMillis()}.jpg"
                val tempFile = File(context.cacheDir, name)
                context.contentResolver.openInputStream(uri)?.use { input ->
                    FileOutputStream(tempFile).use { output ->
                        input.copyTo(output)
                    }
                }
                val err = proxyma_bind.Proxyma_bind.uploadFile(name, tempFile.absolutePath)
                isRunningOnMainThread {
                    tempFile.delete()
                    if (err.isEmpty()) {
                        textVal = name
                        onValueChange(name)
                        Toast.makeText(context, "Image selected & saved to VFS", Toast.LENGTH_SHORT).show()
                    } else {
                        Toast.makeText(context, "Failed to upload image: $err", Toast.LENGTH_LONG).show()
                    }
                }
            }
        }
    }

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(CardGray, shape = RoundedCornerShape(10.dp))
            .border(1.dp, Color.DarkGray, shape = RoundedCornerShape(10.dp))
            .padding(12.dp)
    ) {
        Text(
            text = "${param.name} (${param.type})${if (param.required) " *" else ""}",
            color = Color.White,
            fontWeight = FontWeight.Bold,
            fontSize = 14.sp
        )
        Text(param.description, color = Color.Gray, fontSize = 12.sp)
        Spacer(Modifier.height(8.dp))

        when (param.type) {
            "bool" -> {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Switch(
                        checked = boolVal,
                        onCheckedChange = {
                            boolVal = it
                            onValueChange(it)
                        }
                    )
                    Spacer(Modifier.width(8.dp))
                    Text(if (boolVal) "True" else "False", color = Color.White)
                }
            }
            "int" -> {
                OutlinedTextField(
                    value = textVal,
                    onValueChange = {
                        textVal = it
                        val num = it.toIntOrNull()
                        if (num != null) onValueChange(num)
                    },
                    modifier = Modifier.fillMaxWidth(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    singleLine = true
                )
            }
            "float" -> {
                OutlinedTextField(
                    value = textVal,
                    onValueChange = {
                        textVal = it
                        val num = it.toDoubleOrNull()
                        if (num != null) onValueChange(num)
                    },
                    modifier = Modifier.fillMaxWidth(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
                    singleLine = true
                )
            }
            else -> {
                if (isImageParam) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        OutlinedTextField(
                            value = textVal,
                            onValueChange = {},
                            readOnly = true,
                            modifier = Modifier.weight(1f),
                            placeholder = { Text("No image selected") }
                        )
                        Spacer(Modifier.width(8.dp))
                        Button(
                            onClick = { imagePicker.launch("image/*") },
                            colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary)
                        ) {
                            Text("Pick")
                        }
                    }
                } else {
                    OutlinedTextField(
                        value = textVal,
                        onValueChange = {
                            textVal = it
                            onValueChange(it)
                        },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true
                    )
                }
            }
        }
    }
}
