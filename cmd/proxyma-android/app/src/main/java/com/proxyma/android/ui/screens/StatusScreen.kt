package com.proxyma.android.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Dns
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
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.Peer
import com.proxyma.android.ui.components.Icon
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.formatBytes
import kotlin.concurrent.fixedRateTimer

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
