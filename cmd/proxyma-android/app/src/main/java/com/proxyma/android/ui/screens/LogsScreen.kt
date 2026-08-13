package com.proxyma.android.ui.screens

import androidx.compose.foundation.layout.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.proxyma.android.models.UIDomain
import com.proxyma.android.ui.components.ProjectedActionTable
import com.proxyma.android.ui.components.ScreenTitle

@Composable
fun LogsScreen(telemetryDomain: UIDomain?) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp)
    ) {
        ScreenTitle(telemetryDomain?.title ?: "Node System Logs")
        Spacer(Modifier.height(12.dp))
        telemetryDomain?.action("logs")?.let { action ->
            ProjectedActionTable(action, period = 1000)
        }
    }
}
