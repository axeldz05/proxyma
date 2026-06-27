package com.proxyma.android

import android.Manifest
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.provider.OpenableColumns
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.*
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import java.io.File
import java.io.FileOutputStream
import java.text.DecimalFormat
import kotlin.concurrent.fixedRateTimer
import kotlin.concurrent.thread

// Domain models matching Go outputs
data class Peer(val id: String, val address: String, val online: Boolean)

data class VfsFile(
    val name: String,
    val version: Int,
    val size: Long,
    val hash: String,
    val subscribed: Boolean,
    val hasLocal: Boolean,
    val deleted: Boolean,
    val upSpeed: Double,
    val downSpeed: Double
)

data class LogRecord(val timestamp: String, val level: String, val message: String)

data class ParameterDetail(
    val name: String,
    val type: String,
    val required: Boolean,
    val description: String
)

data class ServiceDetail(
    val name: String,
    val description: String,
    val providerAddress: String,
    val requiredPermissions: List<String>,
    val parameters: List<ParameterDetail>,
    val error: String? = null
)

class MainActivity : ComponentActivity() {

    private var proxymaService: ProxymaService? = null
    private var isBound = false

    private val serviceConnection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, service: IBinder?) {
            val binder = service as ProxymaService.LocalBinder
            proxymaService = binder.getService()
            isBound = true
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            proxymaService = null
            isBound = false
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // Bind Foreground Service
        val intent = Intent(this, ProxymaService::class.java)
        startService(intent)
        bindService(intent, serviceConnection, Context.BIND_AUTO_CREATE)

        // Request notification permission if Android 13+
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 101)
            }
        }

        setContent {
            ProxymaAppTheme {
                MainLayout(proxymaService)
            }
        }
    }

    override fun onDestroy() {
        if (isBound) {
            unbindService(serviceConnection)
            isBound = false
        }
        super.onDestroy()
    }
}

// Harmonized Color Palette for Premium Dark Mode
val DeepGray = Color(0xFF121214)
val CardGray = Color(0xFF1E1E24)
val VioletPrimary = Color(0xFF9D4EDD)
val VioletSecondary = Color(0xFFE0AAFF)
val MintGreen = Color(0xFF52B788)
val AmberWarning = Color(0xFFF3C052)
val ErrorRed = Color(0xFFE63946)

@Composable
fun ProxymaAppTheme(content: @Composable () -> Unit) {
    val colorScheme = darkColorScheme(
        primary = VioletPrimary,
        secondary = VioletSecondary,
        background = DeepGray,
        surface = CardGray,
        error = ErrorRed
    )

    MaterialTheme(
        colorScheme = colorScheme,
        content = content
    )
}

@Composable
fun MainLayout(service: ProxymaService?) {
    var selectedTab by remember { mutableIntStateOf(0) }

    Scaffold(
        bottomBar = {
            NavigationBar(containerColor = DeepGray) {
                NavigationBarItem(
                    selected = selectedTab == 0,
                    onClick = { selectedTab = 0 },
                    icon = { Icon(Icons.Default.Dns, contentDescription = "Status") },
                    label = { Text("Status") }
                )
                NavigationBarItem(
                    selected = selectedTab == 1,
                    onClick = { selectedTab = 1 },
                    icon = { Icon(Icons.Default.Link, contentDescription = "Pairing") },
                    label = { Text("Pairing") }
                )
                NavigationBarItem(
                    selected = selectedTab == 2,
                    onClick = { selectedTab = 2 },
                    icon = { Icon(Icons.Default.FolderOpen, contentDescription = "VFS") },
                    label = { Text("VFS") }
                )
                NavigationBarItem(
                    selected = selectedTab == 3,
                    onClick = { selectedTab = 3 },
                    icon = { Icon(Icons.Default.Terminal, contentDescription = "Services") },
                    label = { Text("Services") }
                )
                NavigationBarItem(
                    selected = selectedTab == 4,
                    onClick = { selectedTab = 4 },
                    icon = { Icon(Icons.Default.Notes, contentDescription = "Logs") },
                    label = { Text("Logs") }
                )
            }
        }
    ) { paddingValues ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(paddingValues)
                .background(DeepGray)
        ) {
            when (selectedTab) {
                0 -> StatusScreen()
                1 -> PairingScreen(service)
                2 -> VFSScreen()
                3 -> ServicesScreen()
                4 -> LogsScreen()
            }
        }
    }
}

// ----------------------------------------------------
// SCREEN 1: STATUS & PEERS
// ----------------------------------------------------
@Composable
fun StatusScreen() {
    var isRunning by remember { mutableStateOf(false) }
    var nodeId by remember { mutableStateOf("-") }
    var address by remember { mutableStateOf("-") }
    var upSpeed by remember { mutableLongStateOf(0L) }
    var downSpeed by remember { mutableLongStateOf(0L) }
    var totalSent by remember { mutableLongStateOf(0L) }
    var totalRecv by remember { mutableLongStateOf(0L) }
    var peersJson by remember { mutableStateOf("[]") }

    // Run interval updates
    DisposableEffect(Unit) {
        val timer = fixedRateTimer(period = 2000) {
            try {
                isRunning = proxyma_bind.Proxyma_bind.isNodeRunning()
                if (isRunning) {
                    nodeId = proxyma_bind.Proxyma_bind.getNodeID()
                    address = proxyma_bind.Proxyma_bind.getNodeAddress()
                    upSpeed = proxyma_bind.Proxyma_bind.getUploadSpeed()
                    downSpeed = proxyma_bind.Proxyma_bind.getDownloadSpeed()
                    totalSent = proxyma_bind.Proxyma_bind.getTotalSent()
                    totalRecv = proxyma_bind.Proxyma_bind.getTotalReceived()
                    peersJson = proxyma_bind.Proxyma_bind.getPeersJson()
                } else {
                    nodeId = "-"
                    address = "-"
                    upSpeed = 0
                    downSpeed = 0
                    totalSent = 0
                    totalRecv = 0
                    peersJson = "[]"
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
    val peerList: List<Peer> = remember(peersJson) {
        try {
            gson.fromJson<List<Peer>>(peersJson, object : TypeToken<List<Peer>>() {}.type) ?: emptyList()
        } catch (e: Exception) {
            emptyList()
        }
    }

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Text(
                text = "Node Overview",
                fontSize = 24.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )
        }

        // Status Card
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardGray),
                shape = RoundedCornerShape(12.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.SpaceBetween,
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text("Daemon Status", fontWeight = FontWeight.Bold, color = Color.Gray)
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Box(
                                modifier = Modifier
                                    .size(10.dp)
                                    .clip(RoundedCornerShape(5.dp))
                                    .background(if (isRunning) MintGreen else ErrorRed)
                            )
                            Spacer(Modifier.width(8.dp))
                            Text(
                                if (isRunning) "ONLINE" else "OFFLINE",
                                color = if (isRunning) MintGreen else ErrorRed,
                                fontWeight = FontWeight.ExtraBold
                            )
                        }
                    }
                    Spacer(Modifier.height(12.dp))
                    Text("Node ID: $nodeId", color = Color.White, fontSize = 15.sp)
                    Spacer(Modifier.height(4.dp))
                    Text("Address: $address", color = Color.White, fontSize = 15.sp)
                }
            }
        }

        // Bandwidth Speed Card
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardGray),
                shape = RoundedCornerShape(12.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Bandwidth Traffic", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(Modifier.height(12.dp))
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        Column {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.ArrowUpward, contentDescription = "Upload", tint = VioletPrimary, size = 18.dp)
                                Spacer(Modifier.width(4.dp))
                                Text("Upload Speed", color = Color.Gray)
                            }
                            Text(
                                formatBytes(upSpeed) + "/s",
                                fontSize = 18.sp,
                                fontWeight = FontWeight.Bold,
                                color = Color.White
                            )
                            Text("Total sent: " + formatBytes(totalSent), fontSize = 12.sp, color = Color.Gray)
                        }
                        Column {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.ArrowDownward, contentDescription = "Download", tint = MintGreen, size = 18.dp)
                                Spacer(Modifier.width(4.dp))
                                Text("Download Speed", color = Color.Gray)
                            }
                            Text(
                                formatBytes(downSpeed) + "/s",
                                fontSize = 18.sp,
                                fontWeight = FontWeight.Bold,
                                color = Color.White
                            )
                            Text("Total received: " + formatBytes(totalRecv), fontSize = 12.sp, color = Color.Gray)
                        }
                    }
                }
            }
        }

        // Peers Header
        item {
            Text(
                text = "Active Peers (${peerList.size})",
                fontSize = 18.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )
        }

        if (peerList.isEmpty()) {
            item {
                Text(
                    "No peers connected currently.",
                    color = Color.Gray,
                    modifier = Modifier.fillMaxWidth(),
                    textAlign = TextAlign.Center
                )
            }
        } else {
            items(peerList) { peer ->
                Card(
                    colors = CardDefaults.cardColors(containerColor = CardGray),
                    shape = RoundedCornerShape(8.dp),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.SpaceBetween
                    ) {
                        Column {
                            Text(peer.id, fontWeight = FontWeight.Bold, color = Color.White)
                            Text(peer.address, fontSize = 12.sp, color = Color.Gray)
                        }
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Box(
                                modifier = Modifier
                                    .size(8.dp)
                                    .clip(RoundedCornerShape(4.dp))
                                    .background(if (peer.online) MintGreen else Color.Gray)
                            )
                            Spacer(Modifier.width(6.dp))
                            Text(
                                if (peer.online) "Active" else "Offline",
                                color = if (peer.online) MintGreen else Color.Gray,
                                fontSize = 12.sp
                            )
                        }
                    }
                }
            }
        }
    }
}

// Helper to format speeds & sizes
fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0 B"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    val digitGroups = (Math.log10(bytes.toDouble()) / Math.log10(1000.0)).toInt()
    return DecimalFormat("#,##0.1").format(bytes / Math.pow(1000.0, digitGroups.toDouble())) + " " + units[digitGroups]
}

// Helper to construct Icon sizing modifiers
@Composable
fun Icon(imageVector: androidx.compose.ui.graphics.vector.ImageVector, contentDescription: String, tint: Color, size: androidx.compose.ui.unit.Dp) {
    Icon(imageVector = imageVector, contentDescription = contentDescription, tint = tint, modifier = Modifier.size(size))
}

// ----------------------------------------------------
// SCREEN 2: PAIRING (JOIN & INVITE)
// ----------------------------------------------------
@Composable
fun PairingScreen(service: ProxymaService?) {
    var tokenInput by remember { mutableStateOf("") }
    var optionalIdInput by remember { mutableStateOf("") }
    var portInput by remember { mutableStateOf("8080") }
    var generatedToken by remember { mutableStateOf("") }
    var isLoading by remember { mutableStateOf(false) }

    val context = LocalContext.current

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Text(
                text = "Pairing Controller",
                fontSize = 24.sp,
                fontWeight = FontWeight.Bold,
                color = Color.White
            )
        }

        // Join Existing Cluster Section
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardGray),
                shape = RoundedCornerShape(12.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Join Existing Cluster", fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
                    Spacer(Modifier.height(12.dp))

                    OutlinedTextField(
                        value = tokenInput,
                        onValueChange = { tokenInput = it },
                        label = { Text("Smart Token") },
                        placeholder = { Text("Paste invite token here") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true
                    )
                    Spacer(Modifier.height(8.dp))

                    OutlinedTextField(
                        value = optionalIdInput,
                        onValueChange = { optionalIdInput = it },
                        label = { Text("Node ID (optional)") },
                        placeholder = { Text("Auto-generated if empty") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true
                    )
                    Spacer(Modifier.height(8.dp))

                    OutlinedTextField(
                        value = portInput,
                        onValueChange = { portInput = it },
                        label = { Text("Listening Port") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number)
                    )
                    Spacer(Modifier.height(16.dp))

                    Button(
                        onClick = {
                            if (tokenInput.isEmpty()) {
                                Toast.makeText(context, "Token is required", Toast.LENGTH_SHORT).show()
                                return@Button
                            }
                            isLoading = true
                            thread {
                                try {
                                    val path = service?.getStoragePath() ?: File(context.filesDir, "proxyma_data").absolutePath
                                    val err = proxyma_bind.Proxyma_bind.joinCluster(path, tokenInput, optionalIdInput, portInput)
                                    isRunningOnMainThread {
                                        isLoading = false
                                        if (err.isNotEmpty()) {
                                            Toast.makeText(context, "Failed: $err", Toast.LENGTH_LONG).show()
                                        } else {
                                            Toast.makeText(context, "Joined cluster successfully!", Toast.LENGTH_SHORT).show()
                                            tokenInput = ""
                                            optionalIdInput = ""
                                        }
                                    }
                                } catch (e: Exception) {
                                    isRunningOnMainThread {
                                        isLoading = false
                                        Toast.makeText(context, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                                    }
                                }
                            }
                        },
                        colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                        modifier = Modifier.fillMaxWidth(),
                        enabled = !isLoading
                    ) {
                        if (isLoading) {
                            CircularProgressIndicator(color = Color.White, modifier = Modifier.size(24.dp))
                        } else {
                            Text("Pair with Cluster", fontWeight = FontWeight.Bold)
                        }
                    }
                }
            }
        }

        // Generate Invite Token Section
        item {
            Card(
                colors = CardDefaults.cardColors(containerColor = CardGray),
                shape = RoundedCornerShape(12.dp),
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Invite Peer", fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
                    Spacer(Modifier.height(12.dp))

                    Button(
                        onClick = {
                            try {
                                val token = proxyma_bind.Proxyma_bind.generateInviteToken()
                                if (token.startsWith("error:")) {
                                    Toast.makeText(context, token, Toast.LENGTH_LONG).show()
                                } else {
                                    generatedToken = token
                                }
                            } catch (e: Exception) {
                                Toast.makeText(context, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                            }
                        },
                        colors = ButtonDefaults.buttonColors(containerColor = MintGreen),
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text("Generate Invite Token", fontWeight = FontWeight.Bold)
                    }

                    if (generatedToken.isNotEmpty()) {
                        Spacer(Modifier.height(12.dp))
                        OutlinedTextField(
                            value = generatedToken,
                            onValueChange = {},
                            readOnly = true,
                            label = { Text("Smart Invite Token") },
                            modifier = Modifier.fillMaxWidth(),
                            trailingIcon = {
                                IconButton(onClick = {
                                    val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
                                    val clip = android.content.ClipData.newPlainText("proxyma_token", generatedToken)
                                    clipboard.setPrimaryClip(clip)
                                    Toast.makeText(context, "Token copied!", Toast.LENGTH_SHORT).show()
                                }) {
                                    Icon(Icons.Default.ContentCopy, contentDescription = "Copy")
                                }
                            }
                        )
                    }
                }
            }
        }
    }
}

// ----------------------------------------------------
// SCREEN 3: VFS FILE MANAGER (WITH FILEPROVIDER)
// ----------------------------------------------------
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

    // Launcher for file picking (uploads)
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
                        tempFile.delete() // clean up cache file
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

            // Bandwidth details for active vfs files
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

            // Action Buttons
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                // Subscribe / Unsubscribe Button
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

                // Open File Button (NATIVE FILEPROVIDER APPROACH)
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

                // Delete Cache Button
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

// Native File Provider Opening
fun openFileNatively(context: Context, path: String, name: String) {
    try {
        val file = File(path)
        if (!file.exists()) {
            Toast.makeText(context, "Blob does not exist", Toast.LENGTH_SHORT).show()
            return
        }

        // Android's FileProvider requires sharing paths configured in file_paths.xml.
        // We will configure a provider mapping 'proxyma_blobs' to copy or share this blob.
        // To be safe and compliant, we can copy the blob to the app's cache directory under its original name,
        // and share that via the FileProvider! This also ensures correct MIME type mapping.
        val cacheSharedFile = File(context.cacheDir, name)
        file.inputStream().use { input ->
            cacheSharedFile.outputStream().use { output ->
                input.copyTo(output)
            }
        }

        val authority = "${context.packageName}.fileprovider"
        val uri = FileProvider.getUriForFile(context, authority, cacheSharedFile)

        val mimeType = context.contentResolver.getType(uri) ?: "*/*"
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mimeType)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        context.startActivity(Intent.createChooser(intent, "Open with"))
    } catch (e: Exception) {
        Toast.makeText(context, "Error opening file: ${e.message}", Toast.LENGTH_LONG).show()
    }
}

// Helper to query selected URI filename
fun getFileName(context: Context, uri: Uri): String? {
    var result: String? = null
    if (uri.scheme == "content") {
        val cursor = context.contentResolver.query(uri, null, null, null, null)
        cursor?.use {
            if (it.moveToFirst()) {
                val index = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (index != -1) {
                    result = it.getString(index)
                }
            }
        }
    }
    if (result == null) {
        result = uri.path
        val cut = result?.lastIndexOf('/') ?: -1
        if (cut != -1) {
            result = result?.substring(cut + 1)
        }
    }
    return result
}

// ----------------------------------------------------
// SCREEN 4: COMPUTE SERVICES SCREEN
// ----------------------------------------------------
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

    val context = LocalContext.current

    if (selectedService == null) {
        // List Services
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
        // Service Details & Runner
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

    // Map to hold inputs
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

        // Required Permissions Block
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
                                // Show execution results modal or Toast
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

    // Image/Gallery Picker
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
                // Save it to VFS first
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

// ----------------------------------------------------
// SCREEN 5: LOG STREAM VIEWER
// ----------------------------------------------------
@Composable
fun LogsScreen() {
    var logsJson by remember { mutableStateOf("[]") }
    var showInfo by remember { mutableStateOf(true) }
    var showWarn by remember { mutableStateOf(true) }
    var showError by remember { mutableStateOf(true) }

    DisposableEffect(Unit) {
        val timer = fixedRateTimer(period = 1000) {
            try {
                if (proxyma_bind.Proxyma_bind.isNodeRunning()) {
                    logsJson = proxyma_bind.Proxyma_bind.getLogsJson()
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
    val fullLogs: List<LogRecord> = remember(logsJson) {
        try {
            gson.fromJson<List<LogRecord>>(logsJson, object : TypeToken<List<LogRecord>>() {}.type) ?: emptyList()
        } catch (e: Exception) {
            emptyList()
        }
    }

    val filteredLogs = remember(fullLogs, showInfo, showWarn, showError) {
        fullLogs.filter { log ->
            when (log.level) {
                "INFO" -> showInfo
                "WARN" -> showWarn
                "ERROR" -> showError
                else -> true
            }
        }.reversed() // newest first
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp)
    ) {
        Text(
            text = "Node System Logs",
            fontSize = 24.sp,
            fontWeight = FontWeight.Bold,
            color = Color.White
        )
        Spacer(Modifier.height(12.dp))

        // Level Filters row
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(checked = showInfo, onCheckedChange = { showInfo = it })
                Text("Info", color = Color.White)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(checked = showWarn, onCheckedChange = { showWarn = it })
                Text("Warning", color = AmberWarning)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(checked = showError, onCheckedChange = { showError = it })
                Text("Error", color = ErrorRed)
            }
        }

        Spacer(Modifier.height(12.dp))

        if (filteredLogs.isEmpty()) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Text("No matching logs.", color = Color.Gray)
            }
        } else {
            LazyColumn(
                modifier = Modifier
                    .fillMaxSize()
                    .background(Color.Black, shape = RoundedCornerShape(8.dp))
                    .padding(8.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                items(filteredLogs) { log ->
                    Row(modifier = Modifier.fillMaxWidth()) {
                        Text(
                            text = "[${log.timestamp}] ",
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            color = Color.Gray
                        )
                        val badgeColor = when (log.level) {
                            "ERROR" -> ErrorRed
                            "WARN" -> AmberWarning
                            else -> MintGreen
                        }
                        Text(
                            text = "${log.level} ",
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            color = badgeColor,
                            fontWeight = FontWeight.Bold
                        )
                        Text(
                            text = log.message,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            color = Color.LightGray
                        )
                    }
                }
            }
        }
    }
}

// Thread context switcher
fun isRunningOnMainThread(action: () -> Unit) {
    android.os.Handler(android.os.Looper.getMainLooper()).post(action)
}
