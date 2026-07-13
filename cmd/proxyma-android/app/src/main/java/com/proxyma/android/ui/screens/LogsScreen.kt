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
import com.proxyma.android.models.LogRecord
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*

@Composable
fun LogsScreen(telemetryDomain: Map<String, Any>?) {
    val fullLogs by rememberPolledParsedState(1000, emptyList<LogRecord>()) {
        proxyma_bind.Proxyma_bind.getLogsJson()
    }
    var showInfo by remember { mutableStateOf(true) }
    var showWarn by remember { mutableStateOf(true) }
    var showError by remember { mutableStateOf(true) }

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
        ScreenTitle((telemetryDomain?.get("title") as? String) ?: "Node System Logs")
        Spacer(Modifier.height(12.dp))

        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            LogFilterCheckbox("Info", showInfo, Color.White) { showInfo = it }
            LogFilterCheckbox("Warning", showWarn, AmberWarning) { showWarn = it }
            LogFilterCheckbox("Error", showError, ErrorRed) { showError = it }
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

@Composable
private fun LogFilterCheckbox(
    label: String,
    checked: Boolean,
    tint: Color,
    onCheckedChange: (Boolean) -> Unit
) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Checkbox(checked = checked, onCheckedChange = onCheckedChange)
        Text(label, color = tint)
    }
}
