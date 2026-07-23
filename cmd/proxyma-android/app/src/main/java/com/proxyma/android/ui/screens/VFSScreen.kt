package com.proxyma.android.ui.screens

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.DeleteSweep
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material.icons.filled.UploadFile
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.models.VfsFile
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import kotlin.concurrent.thread

@Composable
fun VFSScreen(storageDomain: Map<String, Any>?) {
    val filesList by rememberPolledParsedState(2000, emptyList<VfsFile>()) {
        proxyma_bind.Proxyma_bind.getVFSFilesJson()
    }
    var isSyncing by remember { mutableStateOf(false) }
    val context = LocalContext.current

    val filePickerLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetContent()
    ) { uri: Uri? ->
        if (uri != null) {
            uploadUriToVfs(
                context = context,
                uri = uri,
                onStart = { isSyncing = true },
                onComplete = { result ->
                    isSyncing = false
                    result.onSuccess { msg ->
                        context.toast(msg)
                    }
                    result.onFailure { err ->
                        context.toast("Upload failed: ${err.message}", long = true)
                    }
                }
            )
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            ScreenTitle((storageDomain?.get("title") as? String) ?: "VFS File Manager")

            Row {
                IconButton(
                    onClick = {
                        executeGoCall(
                            context = context,
                            onStart = { isSyncing = true },
                            onComplete = { isSyncing = false },
                            action = { proxyma_bind.Proxyma_bind.syncVFS() }
                        ) {
                            context.toast("Synchronization triggered successfully.")
                        }
                    },
                    enabled = !isSyncing
                ) {
                    if (isSyncing) {
                        CircularProgressIndicator(modifier = Modifier.size(24.dp))
                    } else {
                        Icon(Icons.Default.Sync, contentDescription = "Sync", tint = VioletSecondary)
                    }
                }

                IconButton(
                    onClick = { filePickerLauncher.launch("*/*") }
                ) {
                    Icon(Icons.Default.UploadFile, contentDescription = "Upload", tint = MintGreen)
                }
            }
        }

        Spacer(Modifier.height(16.dp))

        if (filesList.isEmpty()) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Text("No files in VFS topology.", color = Color.Gray)
            }
        } else {
            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                items(filesList, key = { it.name }) { file ->
                    VFSFileCard(file)
                }
            }
        }
    }
}

@Composable
fun VFSFileCard(file: VfsFile) {
    val context = LocalContext.current
    var isActionRunning by remember { mutableStateOf(false) }

    ProxymaCard {
        Column(modifier = Modifier.padding(16.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(file.name, fontWeight = FontWeight.Bold, color = Color.White, fontSize = 16.sp)
                    Spacer(Modifier.height(2.dp))
                    Text(
                        "Version ${file.version} • ${formatBytes(file.size)}",
                        fontSize = 12.sp,
                        color = Color.Gray
                    )
                }
                Box(
                    modifier = Modifier
                        .clip(RoundedCornerShape(4.dp))
                        .background(if (file.hasLocal) MintGreen.copy(alpha = 0.2f) else VioletPrimary.copy(alpha = 0.2f))
                        .padding(horizontal = 8.dp, vertical = 4.dp)
                ) {
                    Text(
                        if (file.hasLocal) "Local" else "Remote",
                        color = if (file.hasLocal) MintGreen else VioletSecondary,
                        fontSize = 11.sp,
                        fontWeight = FontWeight.Bold
                    )
                }
            }

            if (file.upSpeed > 0 || file.downSpeed > 0) {
                Spacer(Modifier.height(8.dp))
                Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    if (file.upSpeed > 0) {
                        Text("Up: ${formatBytes(file.upSpeed.toLong())}/s", fontSize = 12.sp, color = VioletSecondary)
                    }
                    if (file.downSpeed > 0) {
                        Text("Down: ${formatBytes(file.downSpeed.toLong())}/s", fontSize = 12.sp, color = MintGreen)
                    }
                }
            }

            Spacer(Modifier.height(12.dp))

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Button(
                    onClick = {
                        executeGoCall(
                            context = context,
                            onStart = { isActionRunning = true },
                            onComplete = { isActionRunning = false },
                            action = { proxyma_bind.Proxyma_bind.setSubscription(file.name, !file.subscribed) }
                        )
                    },
                    colors = ButtonDefaults.buttonColors(
                        containerColor = if (file.subscribed) Color.DarkGray else VioletPrimary
                    ),
                    modifier = Modifier.weight(1f),
                    enabled = !isActionRunning
                ) {
                    Text(if (file.subscribed) "Unsubscribe" else "Subscribe", fontSize = 12.sp, fontWeight = FontWeight.Bold)
                }

                Button(
                    onClick = {
                        executeGoCall(
                            context = context,
                            onStart = { isActionRunning = true },
                            onComplete = { isActionRunning = false },
                            action = {
                                if (!file.hasLocal) {
                                    val err = proxyma_bind.Proxyma_bind.fetchFileOnDemand(file.name)
                                    if (err.isNotEmpty()) {
                                        throw java.lang.Exception(err)
                                    }
                                }
                                ""
                            }
                        ) {
                            val localPath = proxyma_bind.Proxyma_bind.getLocalBlobPath(file.hash)
                            if (localPath.isEmpty()) {
                                context.toast("Local file not found.")
                            } else {
                                openFileNatively(context, localPath, file.name)
                            }
                        }
                    },
                    colors = ButtonDefaults.buttonColors(containerColor = MintGreen),
                    modifier = Modifier.weight(1f),
                    enabled = !isActionRunning
                ) {
                    if (isActionRunning) {
                        CircularProgressIndicator(modifier = Modifier.size(16.dp), color = Color.Black)
                    } else {
                        Text("Open", fontSize = 12.sp, fontWeight = FontWeight.Bold)
                    }
                }

                if (file.hasLocal) {
                    IconButton(
                        onClick = {
                            executeGoCall(
                                context = context,
                                onStart = { isActionRunning = true },
                                onComplete = { isActionRunning = false },
                                action = { proxyma_bind.Proxyma_bind.deleteLocalCache(file.name) }
                            )
                        },
                        enabled = !isActionRunning
                    ) {
                        Icon(Icons.Default.DeleteSweep, contentDescription = "Purge Cache", tint = ErrorRed)
                    }
                }
            }
        }
    }
}
