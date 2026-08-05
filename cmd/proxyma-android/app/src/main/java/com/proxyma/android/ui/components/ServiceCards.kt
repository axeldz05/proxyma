package com.proxyma.android.ui.components

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.CloudQueue
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.FormParameter
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.taskStatusColor

@Composable
fun RunTaskDialog(
    targetName: String,
    isPipeline: Boolean,
    parameterSpecs: List<FormParameter>,
    onDismiss: () -> Unit,
    onExecute: (payloadMap: Map<String, Any>) -> Unit
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Run ${if (isPipeline) "Pipeline" else "Service"}: $targetName") },
        text = {
            DynamicActionForm(
                parameters = parameterSpecs,
                submitButtonText = "Execute",
                localFilePath = true,
                enableCamera = true,
                onSubmit = { inputs, onComplete ->
                    onExecute(inputs)
                    onComplete(Result.success(""))
                }
            )
        },
        confirmButton = {},
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text("Cancel")
            }
        }
    )
}

@Composable
fun TaskLogCardItem(
    task: FileTask,
    onClick: () -> Unit,
    onOpenResult: (path: String, outputName: String) -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier
            .width(260.dp)
            .clickable(onClick = onClick)
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text(
                    text = task.service.uppercase(),
                    fontWeight = FontWeight.Bold,
                    fontSize = 14.sp,
                    color = VioletSecondary
                )
                val statusColor = taskStatusColor(task.status)
                Text(
                    text = task.status.uppercase(),
                    fontWeight = FontWeight.Bold,
                    fontSize = 11.sp,
                    color = statusColor
                )
            }
            Spacer(modifier.height(6.dp))
            Text("Input: ${task.input}", fontSize = 12.sp, color = Color.LightGray, maxLines = 1)
            Text("Output: ${task.output}", fontSize = 12.sp, color = Color.LightGray, maxLines = 1)
            if (task.status == "completed" && task.resultPath != null) {
                Spacer(modifier.height(8.dp))
                Button(
                    onClick = { onOpenResult(task.resultPath, task.output) },
                    colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(32.dp),
                    contentPadding = PaddingValues(0.dp)
                ) {
                    Text("Open Result", fontSize = 12.sp, color = Color.White)
                }
            } else if (task.status == "failed" && task.error != null) {
                Spacer(modifier.height(6.dp))
                Text(task.error, fontSize = 11.sp, color = Color.Red, maxLines = 2)
            }
        }
    }
}

@Composable
fun PipelineCardItem(
    pipeline: PipelineSchema,
    onRun: () -> Unit,
    onEdit: () -> Unit,
    onClone: () -> Unit,
    onDelete: () -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column {
                    Text(pipeline.id, fontWeight = FontWeight.Bold, color = Color.White, fontSize = 16.sp)
                    Text("Version: ${pipeline.version} | Steps: ${pipeline.steps.size}", fontSize = 12.sp, color = Color.Gray)
                }

                Row {
                    IconButton(onClick = onRun) {
                        Icon(Icons.Default.PlayArrow, contentDescription = "Run Pipeline", tint = MintGreen)
                    }
                    IconButton(onClick = onEdit) {
                        Icon(Icons.Default.Edit, contentDescription = "Edit Pipeline", tint = VioletSecondary)
                    }
                    IconButton(onClick = onClone) {
                        Icon(Icons.Default.ContentCopy, contentDescription = "Clone & Localize Pipeline", tint = MintGreen)
                    }
                    IconButton(onClick = onDelete) {
                        Icon(Icons.Default.Delete, contentDescription = "Delete Pipeline", tint = Color.Red)
                    }
                }
            }
        }
    }
}

@Composable
fun ServiceCardItem(
    svcName: String,
    onClick: () -> Unit,
    onRun: () -> Unit
) {
    ProxymaCard(
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick)
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
                Spacer(modifier.width(12.dp))
                Text(svcName, fontWeight = FontWeight.Bold, color = Color.White)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onRun) {
                    Icon(Icons.Default.PlayArrow, contentDescription = "Run Service", tint = MintGreen)
                }
                Icon(Icons.Default.ChevronRight, contentDescription = "Open Details", tint = Color.Gray)
            }
        }
    }
}
