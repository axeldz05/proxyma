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
import androidx.compose.material.icons.automirrored.filled.Notes
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.FolderOpen
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import androidx.lifecycle.viewmodel.compose.viewModel
import com.proxyma.android.models.*
import com.proxyma.android.ui.screens.*
import com.proxyma.android.ui.theme.DeepGray
import com.proxyma.android.ui.theme.ProxymaAppTheme
import com.proxyma.android.utils.parseUISchema
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {

    private val proxymaService = ObservableBindingState<ProxymaService>()
    private var isBound = false

    private val serviceConnection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, service: IBinder?) {
            val binder = service as ProxymaService.LocalBinder
            proxymaService.bind(binder.getService())
            isBound = true
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            proxymaService.unbind()
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
                MainLayout(proxymaService.state.value)
            }
        }
    }

    override fun onDestroy() {
        if (isBound) {
            unbindService(serviceConnection)
            isBound = false
        }
        proxymaService.unbind()
        super.onDestroy()
    }
}

@Composable
fun MainLayout(service: ProxymaService?) {
    var selectedTab by remember { mutableIntStateOf(0) }
    val servicesViewModel: ServicesViewModel = viewModel()

    val uiDomains by produceState<List<UIDomain>>(initialValue = emptyList()) {
        value = withContext(Dispatchers.IO) {
            try {
                parseUISchema(proxyma_bind.Proxyma_bind.getUISchemaJSONForSurface("android"))
            } catch (error: Exception) {
                error.printStackTrace()
                emptyList()
            }
        }
    }

    val telemetryDomain = uiDomains.find { domain -> domain.name == "telemetry" }
    val clusterDomain = uiDomains.find { domain -> domain.name == "cluster" }
    val storageDomain = uiDomains.find { domain -> domain.name == "storage" }
    val serviceDomain = uiDomains.find { domain -> domain.name == "service" }
    val peersDomain = uiDomains.find { domain -> domain.name == "peers" }

    val tabs = remember {
        listOf(
            NavigationTab(0, "Status", Icons.Default.Dns, "Status"),
            NavigationTab(1, "Pairing", Icons.Default.Link, "Pairing"),
            NavigationTab(2, "VFS", Icons.Default.FolderOpen, "VFS"),
            NavigationTab(3, "Services", Icons.Default.Terminal, "Services"),
            NavigationTab(4, "Collab", Icons.Default.Edit, "Collab Editor"),
            NavigationTab(5, "Logs", Icons.AutoMirrored.Filled.Notes, "Logs")
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
                3 -> ServicesScreen(serviceDomain, servicesViewModel)
                4 -> CollabEditorScreen()
                5 -> LogsScreen(telemetryDomain)
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
