package com.proxyma.android.ui.screens

import android.net.Uri
import android.widget.Toast
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
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.VfsFile
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import java.io.File
import java.io.FileOutputStream
import kotlin.concurrent.fixedRateTimer
import kotlin.concurrent.thread

@Composable
fun VFSScreen() {
    var vfsFilesJson by remember { mutableStateOf("[]") }
    var isSyncing by remember { mutableStateOf(false) }

    DisposableEffect(Unit) {
        val timer = fixedRateTimer(period = 2000) {
            try {
                if (proxyma_bind.Proxyma_bind.isNodeRunning()) {
                    vfsFilesJson = proxyma_bind.Proxyma_bind.getVFSFilesJson()
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
    val filesList: List<VfsFile> = remember(vfsFilesJson) {
        try {
            gson.fromJson<List<VfsFile>>(vfsFilesJson, object : TypeToken<List<VfsFile>>() {}.type) ?: emptyList()
        } catch (e: Exception) {
            emptyList()
        }
    }

    val context = LocalContext.current

    val filePickerLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetContent()
    ) { uri: Uri? ->
        if (uri != null) {
            isSyncing = true
            thread {
                try {
                    val name = getFileName(context, uri) ?: "upload_${System.currentTimeMillis()}"
                    val tempFile = File(context.cacheDir, name)
                    val input = context.contentResolver.openInputStream(uri)
                    val output = FileOutputStream(tempFile)
                    input?.use { inStream ->
                        output.use { outStream ->
                            inStream.copyTo(outStream)
                        }
                    }
                    val err = proxyma_bind.Proxyma_bind.uploadFile(name, tempFile.absolutePath)
                    isRunningOnMainThread {
                        isSyncing = false
                        tempFile.delete()
                        if (err.isNotEmpty()) {
                            Toast.makeText(context, "Upload failed: $err", Toast.LENGTH_LONG).show()
                        } else {
                            Toast.makeText(context, "File uploaded successfully!", Toast.LENGTH_SHORT).show()
                        }
                    }
                } catch (e: Exception) {
                    isRunningOnMainThread {
                        isSyncing = false
                        Toast.makeText(context, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                    }
                }
            }
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
            Text(
                text = "VFS File Manager",
                fontSize = 24.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )

            Row {
                IconButton(
                    onClick = {
                        isSyncing = true
                        thread {
                            val err = proxyma_bind.Proxyma_bind.syncVFS()
                            isRunningOnMainThread {
                                isSyncing = false
                                if (err.isNotEmpty()) {
                                    Toast.makeText(context, "Sync failed: $err", Toast.LENGTH_LONG).show()
                                } else {
                                    Toast.makeText(context, "Sync complete!", Toast.LENGTH_SHORT).show()
                                }
                            }
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
                items(filesList) { file ->
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

    Card(
        colors = CardDefaults.cardColors(containerColor = CardGray),
        shape = RoundedCornerShape(12.dp),
        modifier = Modifier.fillMaxWidth()
    ) {
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
                        isActionRunning = true
                        thread {
                            proxyma_bind.Proxyma_bind.setSubscription(file.name, !file.subscribed)
                            isRunningOnMainThread { isActionRunning = false }
                        }
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
                        if (!file.hasLocal) {
                            Toast.makeText(context, "Subscribe first to download file locally.", Toast.LENGTH_SHORT).show()
                            return@Button
                        }
                        val localPath = proxyma_bind.Proxyma_bind.getLocalBlobPath(file.hash)
                        if (localPath.isEmpty()) {
                            Toast.makeText(context, "Local file not found.", Toast.LENGTH_SHORT).show()
                            return@Button
                        }
                        openFileNatively(context, localPath, file.name)
                    },
                    colors = ButtonDefaults.buttonColors(containerColor = MintGreen),
                    modifier = Modifier.weight(1f),
                    enabled = !isActionRunning
                ) {
                    Text("Open", fontSize = 12.sp, fontWeight = FontWeight.Bold)
                }

                if (file.hasLocal) {
                    IconButton(
                        onClick = {
                            isActionRunning = true
                            thread {
                                val err = proxyma_bind.Proxyma_bind.deleteLocalCache(file.name)
                                isRunningOnMainThread {
                                    isActionRunning = false
                                    if (err.isNotEmpty()) {
                                        Toast.makeText(context, "Delete failed: $err", Toast.LENGTH_LONG).show()
                                    }
                                }
                            }
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
