package com.proxyma.android.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.models.Peer
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.components.StatusIndicator
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*

@Suppress("UNCHECKED_CAST")
@Composable
fun StatusScreen(telemetryDomain: Map<String, Any>?, peersDomain: Map<String, Any>?) {
    var isRunning by remember { mutableStateOf(false) }
    var nodeId by remember { mutableStateOf("-") }
    var address by remember { mutableStateOf("-") }
    var upSpeed by remember { mutableLongStateOf(0L) }
    var downSpeed by remember { mutableLongStateOf(0L) }
    var totalSent by remember { mutableLongStateOf(0L) }
    var totalRecv by remember { mutableLongStateOf(0L) }
    
    val peerList by rememberPolledParsedState(2000, emptyList<Peer>()) {
        proxyma_bind.Proxyma_bind.getPeersJson()
    }

    PollState(period = 2000) {
        isRunning = proxyma_bind.Proxyma_bind.isNodeRunning()
        if (isRunning) {
            nodeId = proxyma_bind.Proxyma_bind.getNodeID()
            address = proxyma_bind.Proxyma_bind.getNodeAddress()
            upSpeed = proxyma_bind.Proxyma_bind.getUploadSpeed()
            downSpeed = proxyma_bind.Proxyma_bind.getDownloadSpeed()
            totalSent = proxyma_bind.Proxyma_bind.getTotalSent()
            totalRecv = proxyma_bind.Proxyma_bind.getTotalReceived()
        } else {
            nodeId = "-"
            address = "-"
            upSpeed = 0
            downSpeed = 0
            totalSent = 0
            totalRecv = 0
        }
    }

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            ScreenTitle((telemetryDomain?.get("title") as? String) ?: "Node Overview")
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
                            active = isRunning,
                            activeLabel = "ONLINE",
                            inactiveLabel = "OFFLINE",
                            activeColor = MintGreen,
                            inactiveColor = ErrorRed,
                            dotSize = 10.dp,
                            fontWeight = FontWeight.ExtraBold
                        )
                    }
                    Spacer(Modifier.height(12.dp))
                    Text("Node ID: $nodeId", color = Color.White, fontSize = 15.sp)
                    Spacer(Modifier.height(4.dp))
                    Text("Address: $address", color = Color.White, fontSize = 15.sp)
                }
            }
        }

        item {
            ProxymaCard {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("Bandwidth Traffic", fontWeight = FontWeight.Bold, color = Color.Gray)
                    Spacer(Modifier.height(12.dp))
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        TrafficStatColumn(
                            title = "Upload Speed",
                            speed = upSpeed,
                            total = totalSent,
                            totalPrefix = "Total sent: ",
                            icon = Icons.Default.ArrowUpward,
                            iconColor = VioletPrimary,
                            contentDescription = "Upload"
                        )
                        TrafficStatColumn(
                            title = "Download Speed",
                            speed = downSpeed,
                            total = totalRecv,
                            totalPrefix = "Total received: ",
                            icon = Icons.Default.ArrowDownward,
                            iconColor = MintGreen,
                            contentDescription = "Download"
                        )
                    }
                }
            }
        }

        item {
            Text(
                text = "${(peersDomain?.get("title") as? String) ?: "Active Peers"} (${peerList.size})",
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
                ProxymaCard(shape = RoundedCornerShape(8.dp)) {
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
                        StatusIndicator(
                            active = peer.online,
                            activeLabel = "Active",
                            inactiveLabel = "Offline",
                            activeColor = MintGreen,
                            inactiveColor = Color.Gray,
                            dotSize = 8.dp,
                            fontSize = 12.sp
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun TrafficStatColumn(
    title: String,
    speed: Long,
    total: Long,
    totalPrefix: String,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    iconColor: Color,
    contentDescription: String
) {
    Column {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Icon(
                imageVector = icon,
                contentDescription = contentDescription,
                tint = iconColor,
                size = 18.dp
            )
            Spacer(Modifier.width(4.dp))
            Text(title, color = Color.Gray)
        }
        Text(
            text = formatBytes(speed) + "/s",
            fontSize = 18.sp,
            fontWeight = FontWeight.Bold,
            color = Color.White
        )
        Text(
            text = totalPrefix + formatBytes(total),
            fontSize = 12.sp,
            color = Color.Gray
        )
    }
}
