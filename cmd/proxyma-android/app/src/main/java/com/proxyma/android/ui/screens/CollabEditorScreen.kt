package com.proxyma.android.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.google.gson.Gson
import com.proxyma.android.ui.components.ProxymaCard
import com.proxyma.android.ui.components.ScreenTitle
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.*
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

data class CollabUser(
    val user_id: String = "",
    val user_name: String = "",
    val pos: Int = 0
)

data class CollabChunk(
    val type: String = "",
    val doc_id: String? = null,
    val user_id: String? = null,
    val user_name: String? = null,
    val pos: Int? = null,
    val len: Int? = null,
    val text: String? = null,
    val action: String? = null,
    val content: String? = null,
    val users: Map<String, CollabUser> = emptyMap()
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CollabEditorScreen() {
    val context = LocalContext.current
    var docId by remember { mutableStateOf("shared-doc-1") }
    var userName by remember { mutableStateOf("AndroidUser_${(100..999).random()}") }
    var isConnected by remember { mutableStateOf(false) }
    var documentContent by remember { mutableStateOf("") }
    val activeUsers = remember { mutableStateMapOf<String, CollabUser>() }
    val scope = rememberCoroutineScope()
    var joinJob by remember { mutableStateOf<Job?>(null) }

    fun joinDoc() {
        if (isConnected || joinJob?.isActive == true) return
        val myUserId = "user_${System.currentTimeMillis() % 10000}"
        val joinPayload = mapOf(
            "type" to "join",
            "doc_id" to docId,
            "user_id" to myUserId,
            "user_name" to userName
        )
        val payloadJson = Gson().toJson(joinPayload)

        val startedJob = launchManagedBindStream(
            scope = scope,
            serviceName = "collab_editor",
            payloadJson = payloadJson,
            listenerFactory = { stop ->
                object : proxyma_bind.StreamEventListener {
                    override fun onChunk(chunkJSON: String) {
                        val msg = try {
                            Gson().fromJson(chunkJSON, CollabChunk::class.java)
                        } catch (_: Exception) {
                            null
                        } ?: return
                        scope.launch {
                            when (msg.type) {
                                "snapshot" -> {
                                    isConnected = true
                                    if (msg.content != null) {
                                        documentContent = msg.content
                                    }
                                    activeUsers.clear()
                                    activeUsers.putAll(msg.users.orEmpty())
                                }
                                "user_joined" -> {
                                    activeUsers.clear()
                                    activeUsers.putAll(msg.users.orEmpty())
                                }
                                "user_left" -> {
                                    if (msg.user_id != null) {
                                        activeUsers.remove(msg.user_id)
                                    }
                                }
                                "op" -> {
                                    if (msg.content != null) {
                                        documentContent = msg.content
                                    }
                                }
                            }
                        }
                    }

                    override fun onError(errMsg: String) {
                        val message = bindErrorMessage(errMsg)
                        scope.launch {
                            isConnected = false
                            context.toast("❌ Collab stream error: $message")
                            stop()
                        }
                    }

                    override fun onComplete() {
                        scope.launch {
                            isConnected = false
                            context.toast("Disconnected from collab session.")
                            stop()
                        }
                    }
                }
            },
            onStartFailure = { error ->
                isConnected = false
                context.toast("❌ Collab stream error: ${error.message ?: "failed"}")
            }
        )
        joinJob = startedJob
        startedJob.invokeOnCompletion {
            scope.launch {
                if (joinJob === startedJob) {
                    joinJob = null
                }
            }
        }
    }

    fun sendTextInsert(newText: String) {
        val diffLen = newText.length - documentContent.length
        if (diffLen > 0) {
            // Insertion operation
            val addedText = newText.takeLast(diffLen)
            val pos = documentContent.length
            val opPayload = mapOf(
                "type" to "insert",
                "doc_id" to docId,
                "user_name" to userName,
                "pos" to pos,
                "text" to addedText
            )
            val jsonStr = Gson().toJson(opPayload)
            // Perform streaming update
            launchManagedBindStream(
                scope = scope,
                serviceName = "collab_editor",
                payloadJson = jsonStr,
                listenerFactory = { stop ->
                    object : proxyma_bind.StreamEventListener {
                        override fun onChunk(chunkJSON: String) {}
                        override fun onError(errMsg: String) {
                            val message = bindErrorMessage(errMsg)
                            scope.launch {
                                context.toast("❌ Collab update failed: $message")
                                stop()
                            }
                        }

                        override fun onComplete() {
                            stop()
                        }
                    }
                },
                onStartFailure = { error ->
                    context.toast("❌ Collab update failed: ${error.message ?: "failed"}")
                }
            )
        }
        documentContent = newText
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        ScreenTitle("Real-Time Collab Editor")

        ProxymaCard(shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    OutlinedTextField(
                        value = docId,
                        onValueChange = { docId = it },
                        label = { Text("Document ID", color = Color.Gray) },
                        modifier = Modifier.weight(1f),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedTextColor = Color.White,
                            unfocusedTextColor = Color.White,
                            focusedBorderColor = VioletPrimary,
                            unfocusedBorderColor = Color.Gray
                        )
                    )
                    Spacer(Modifier.width(8.dp))
                    Button(
                        onClick = { joinDoc() },
                        colors = ButtonDefaults.buttonColors(containerColor = if (isConnected) MintGreen else VioletPrimary)
                    ) {
                        Text(if (isConnected) "Connected" else "Join Session", color = Color.White)
                    }
                }

                if (activeUsers.isNotEmpty()) {
                    Text("Online Collaborators:", fontSize = 12.sp, color = Color.LightGray)
                    LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        items(activeUsers.values.toList()) { user ->
                            Surface(
                                shape = RoundedCornerShape(12.dp),
                                color = VioletSecondary.copy(alpha = 0.3f),
                                border = ButtonDefaults.outlinedButtonBorder
                            ) {
                                Text(
                                    text = "👤 ${user.user_name}",
                                    modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp),
                                    fontSize = 12.sp,
                                    color = Color.White
                                )
                            }
                        }
                    }
                }
            }
        }

        // Shared Document Real-Time Editor Box
        ProxymaCard(shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxSize()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Text("Shared Document Text (Google Docs Mode)", fontSize = 14.sp, fontWeight = FontWeight.Bold, color = VioletSecondary)
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = documentContent,
                    onValueChange = { sendTextInsert(it) },
                    modifier = Modifier
                        .fillMaxSize()
                        .background(Color.Black, shape = RoundedCornerShape(6.dp)),
                    textStyle = androidx.compose.ui.text.TextStyle(
                        color = MintGreen,
                        fontSize = 14.sp,
                        fontFamily = FontFamily.Monospace
                    ),
                    placeholder = { Text("Join session to start typing collaboratively...", color = Color.Gray) },
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = VioletPrimary,
                        unfocusedBorderColor = Color.DarkGray
                    )
                )
            }
        }
    }
}
