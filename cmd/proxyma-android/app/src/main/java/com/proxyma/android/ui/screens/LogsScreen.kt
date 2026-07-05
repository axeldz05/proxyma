package com.proxyma.android.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.proxyma.android.models.LogRecord
import com.proxyma.android.ui.theme.*
import kotlin.concurrent.fixedRateTimer

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
        }.reversed()
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
