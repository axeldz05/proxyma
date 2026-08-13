package com.proxyma.android.ui.screens

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.models.UIDomain
import com.proxyma.android.ui.components.ProjectedActionTable
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.components.StatusIndicator
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*

private data class NodeStatusSnapshot(
    val isRunning: Boolean = false,
    val nodeId: String = "-",
    val address: String = "-"
)

@Composable
fun StatusScreen(telemetryDomain: UIDomain?, peersDomain: UIDomain?) {
    var nodeStatus by remember { mutableStateOf(NodeStatusSnapshot()) }

    PollState(
        period = 2000,
        fetchData = {
            if (proxyma_bind.Proxyma_bind.isNodeRunning()) {
                NodeStatusSnapshot(
                    isRunning = true,
                    nodeId = proxyma_bind.Proxyma_bind.getNodeID(),
                    address = proxyma_bind.Proxyma_bind.getNodeAddress()
                )
            } else {
                NodeStatusSnapshot()
            }
        },
        onResult = { nodeStatus = it }
    )

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            ScreenTitle(telemetryDomain?.title ?: "Node Overview")
        }

        item {
            ProxymaCard {
                Column(modifier = Modifier.padding(16.dp)) {
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.SpaceBetween,
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text("Daemon Status", fontWeight = FontWeight.Bold, color = Color.Gray)
                        StatusIndicator(
                            active = nodeStatus.isRunning,
                            activeLabel = "ONLINE",
                            inactiveLabel = "OFFLINE",
                            activeColor = MintGreen,
                            inactiveColor = ErrorRed,
                            dotSize = 10.dp,
                            fontWeight = FontWeight.ExtraBold
                        )
                    }
                    Spacer(Modifier.height(12.dp))
                    Text("Node ID: ${nodeStatus.nodeId}", color = Color.White, fontSize = 15.sp)
                    Spacer(Modifier.height(4.dp))
                    Text("Address: ${nodeStatus.address}", color = Color.White, fontSize = 15.sp)
                }
            }
        }

        telemetryDomain?.action("stats")?.let { action ->
            item { ProjectedActionTable(action) }
        }

        peersDomain?.action("list")?.let { action ->
            item { ProjectedActionTable(action) }
        }
    }
}
