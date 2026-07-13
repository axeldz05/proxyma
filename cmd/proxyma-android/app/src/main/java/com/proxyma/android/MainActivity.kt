package com.proxyma.android

import android.Manifest
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.FolderOpen
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Notes
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.*
import com.proxyma.android.ui.screens.*
import com.proxyma.android.ui.theme.DeepGray
import com.proxyma.android.ui.theme.ProxymaAppTheme

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

        // Set global storage path for Go-mobile bindings
        val path = java.io.File(filesDir, "proxyma_data").absolutePath
        proxyma_bind.Proxyma_bind.setStoragePath(path)

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

@Composable
fun MainLayout(service: ProxymaService?) {
    var selectedTab by remember { mutableIntStateOf(0) }

    val uiDomains = remember {
        try {
            val json = proxyma_bind.Proxyma_bind.getUISchemaJSON()
            Gson().fromJson<List<Map<String, Any>>>(json, object : TypeToken<List<Map<String, Any>>>() {}.type) ?: emptyList()
        } catch (e: Exception) {
            e.printStackTrace()
            emptyList()
        }
    }

    val telemetryDomain = uiDomains.find { domain -> domain["name"] == "telemetry" }
    val clusterDomain = uiDomains.find { domain -> domain["name"] == "cluster" }
    val storageDomain = uiDomains.find { domain -> domain["name"] == "storage" }
    val serviceDomain = uiDomains.find { domain -> domain["name"] == "service" }
    val peersDomain = uiDomains.find { domain -> domain["name"] == "peers" }

    val tabs = remember {
        listOf(
            NavigationTab(0, "Status", Icons.Default.Dns, "Status"),
            NavigationTab(1, "Pairing", Icons.Default.Link, "Pairing"),
            NavigationTab(2, "VFS", Icons.Default.FolderOpen, "VFS"),
            NavigationTab(3, "Services", Icons.Default.Terminal, "Services"),
            NavigationTab(4, "Logs", Icons.Default.Notes, "Logs")
        )
    }

    Scaffold(
        bottomBar = {
            NavigationBar(containerColor = DeepGray) {
                tabs.forEach { tab ->
                    NavigationBarItem(
                        selected = selectedTab == tab.index,
                        onClick = { selectedTab = tab.index },
                        icon = { Icon(tab.icon, contentDescription = tab.contentDescription) },
                        label = { Text(tab.label) }
                    )
                }
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
                0 -> StatusScreen(telemetryDomain, peersDomain)
                1 -> PairingScreen(service, clusterDomain)
                2 -> VFSScreen(storageDomain)
                3 -> ServicesScreen(serviceDomain)
                4 -> LogsScreen(telemetryDomain)
            }
        }
    }
}

private data class NavigationTab(
    val index: Int,
    val label: String,
    val icon: androidx.compose.ui.graphics.vector.ImageVector,
    val contentDescription: String
)
