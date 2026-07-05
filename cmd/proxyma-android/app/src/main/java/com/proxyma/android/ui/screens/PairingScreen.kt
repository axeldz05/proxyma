package com.proxyma.android.ui.screens

import android.content.Context
import android.widget.Toast
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.ProxymaService
import com.proxyma.android.ui.theme.*
import com.proxyma.android.utils.isRunningOnMainThread
import java.io.File
import kotlin.concurrent.thread

@Composable
fun PairingScreen(service: ProxymaService?) {
    var tokenInput by remember { mutableStateOf("") }
    var optionalIdInput by remember { mutableStateOf("") }
    var portInput by remember { mutableStateOf("8080") }
    var generatedToken by remember { mutableStateOf("") }
    var isLoading by remember { mutableStateOf(false) }

    val context = LocalContext.current

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        item {
            Text(
                text = "Pairing Controller",
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
                    Text("Join Existing Cluster", fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
                    Spacer(Modifier.height(12.dp))

                    OutlinedTextField(
                        value = tokenInput,
                        onValueChange = { tokenInput = it },
                        label = { Text("Smart Token") },
                        placeholder = { Text("Paste invite token here") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true
                    )
                    Spacer(Modifier.height(8.dp))

                    OutlinedTextField(
                        value = optionalIdInput,
                        onValueChange = { optionalIdInput = it },
                        label = { Text("Node ID (optional)") },
                        placeholder = { Text("Auto-generated if empty") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true
                    )
                    Spacer(Modifier.height(8.dp))

                    OutlinedTextField(
                        value = portInput,
                        onValueChange = { portInput = it },
                        label = { Text("Listening Port") },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number)
                    )
                    Spacer(Modifier.height(16.dp))

                    Button(
                        onClick = {
                            if (tokenInput.isEmpty()) {
                                Toast.makeText(context, "Token is required", Toast.LENGTH_SHORT).show()
                                return@Button
                            }
                            isLoading = true
                            thread {
                                try {
                                    val path = service?.getStoragePath() ?: File(context.filesDir, "proxyma_data").absolutePath
                                    val err = proxyma_bind.Proxyma_bind.joinCluster(path, tokenInput, optionalIdInput, portInput)
                                    isRunningOnMainThread {
                                        isLoading = false
                                        if (err.isNotEmpty()) {
                                            Toast.makeText(context, "Failed: $err", Toast.LENGTH_LONG).show()
                                        } else {
                                            Toast.makeText(context, "Joined cluster successfully!", Toast.LENGTH_SHORT).show()
                                            tokenInput = ""
                                            optionalIdInput = ""
                                        }
                                    }
                                } catch (e: Exception) {
                                    isRunningOnMainThread {
                                        isLoading = false
                                        Toast.makeText(context, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                                    }
                                }
                            }
                        },
                        colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                        modifier = Modifier.fillMaxWidth(),
                        enabled = !isLoading
                    ) {
                        if (isLoading) {
                            CircularProgressIndicator(color = Color.White, modifier = Modifier.size(24.dp))
                        } else {
                            Text("Pair with Cluster", fontWeight = FontWeight.Bold)
                        }
                    }
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
                    Text("Invite Peer", fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
                    Spacer(Modifier.height(12.dp))

                    Button(
                        onClick = {
                            try {
                                val token = proxyma_bind.Proxyma_bind.generateInviteToken()
                                if (token.startsWith("error:")) {
                                    Toast.makeText(context, token, Toast.LENGTH_LONG).show()
                                } else {
                                    generatedToken = token
                                }
                            } catch (e: Exception) {
                                Toast.makeText(context, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                            }
                        },
                        colors = ButtonDefaults.buttonColors(containerColor = MintGreen),
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text("Generate Invite Token", fontWeight = FontWeight.Bold)
                    }

                    if (generatedToken.isNotEmpty()) {
                        Spacer(Modifier.height(12.dp))
                        OutlinedTextField(
                            value = generatedToken,
                            onValueChange = {},
                            readOnly = true,
                            label = { Text("Smart Invite Token") },
                            modifier = Modifier.fillMaxWidth(),
                            trailingIcon = {
                                IconButton(onClick = {
                                    val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
                                    val clip = android.content.ClipData.newPlainText("proxyma_token", generatedToken)
                                    clipboard.setPrimaryClip(clip)
                                    Toast.makeText(context, "Token copied!", Toast.LENGTH_SHORT).show()
                                }) {
                                    Icon(Icons.Default.ContentCopy, contentDescription = "Copy")
                                }
                            }
                        )
                    }
                }
            }
        }
    }
}
