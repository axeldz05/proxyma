package com.proxyma.android.ui.components

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.proxyma.android.ui.theme.CardGray
import com.proxyma.android.ui.theme.VioletPrimary
import com.proxyma.android.models.FormParameter
import com.proxyma.android.utils.*

@Composable
fun DynamicActionForm(
    parameters: List<FormParameter>,
    submitButtonText: String,
    onSubmit: (inputs: Map<String, Any>, onComplete: (Result<String>) -> Unit) -> Unit
) {
    val context = LocalContext.current
    var isSubmitting by remember { mutableStateOf(false) }

    val validationErrors = remember { mutableStateMapOf<String, String>() }

    val inputs = remember(parameters) {
        val initialMap = mutableStateMapOf<String, Any>()
        parameters.forEach { param ->
            if (param.defaultValue != null) {
                if (param.type == "bool") {
                    initialMap[param.name] = param.defaultValue == "true" || param.defaultValue == "1"
                } else if (param.type == "int") {
                    param.defaultValue.toIntOrNull()?.let { initialMap[param.name] = it }
                } else {
                    initialMap[param.name] = param.defaultValue
                }
            }
        }
        initialMap
    }

    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        if (validationErrors.isNotEmpty()) {
            Surface(
                color = Color(0xFF3E1F24),
                shape = RoundedCornerShape(8.dp),
                border = androidx.compose.foundation.BorderStroke(1.dp, Color(0xFFFF5252)),
                modifier = Modifier.fillMaxWidth()
            ) {
                Row(
                    modifier = Modifier.padding(12.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text("⚠️", fontSize = 18.sp)
                    Spacer(Modifier.width(8.dp))
                    Column {
                        Text(
                            "Parámetros obligatorios faltantes",
                            color = Color(0xFFFF5252),
                            fontWeight = FontWeight.Bold,
                            fontSize = 13.sp
                        )
                        Text(
                            "Por favor completa: ${validationErrors.keys.joinToString(", ")}",
                            color = Color.White,
                            fontSize = 12.sp
                        )
                    }
                }
            }
        }

        parameters.forEach { param ->
            val value = inputs[param.name]
            ParameterInput(
                param = param,
                value = value,
                errorMessage = validationErrors[param.name],
                onValueChange = { newValue ->
                    inputs[param.name] = newValue
                    validationErrors.remove(param.name)
                }
            )
        }

        Spacer(modifier = Modifier.height(4.dp))

        Button(
            onClick = {
                validationErrors.clear()
                for (param in parameters) {
                    if (param.required) {
                        val valStr = inputs[param.name]?.toString() ?: ""
                        if (valStr.trim().isEmpty()) {
                            validationErrors[param.name] = "⚠️ ${param.name} es un campo obligatorio"
                        }
                    }
                }

                if (validationErrors.isNotEmpty()) {
                    context.toast("Por favor completa los campos requeridos")
                    return@Button
                }

                isSubmitting = true
                onSubmit(inputs.toMap()) { result ->
                    isSubmitting = false
                    result.onSuccess { msg ->
                        context.toast(msg)
                    }
                    result.onFailure { err ->
                        context.toast(err.message ?: "Action failed", long = true)
                    }
                }
            },
            colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
            modifier = Modifier.fillMaxWidth(),
            enabled = !isSubmitting
        ) {
            if (isSubmitting) {
                CircularProgressIndicator(color = Color.White, modifier = Modifier.size(24.dp))
            } else {
                Text(submitButtonText, fontWeight = FontWeight.Bold)
            }
        }
    }
}

@Composable
fun ParameterInput(
    param: FormParameter,
    value: Any?,
    errorMessage: String? = null,
    onValueChange: (Any) -> Unit
) {
    val context = LocalContext.current
    
    val stringValue = (value ?: "").toString()
    val boolValue = value as? Boolean ?: false

    val isFilePicker = remember(param) {
        param.type == "file" || param.uiHint == "file_picker" || param.uiHint == "image_picker"
    }

    val isImagePicker = remember(param) {
        param.uiHint == "image_picker"
    }

    var isFileUploading by remember { mutableStateOf(false) }

    val filePickerLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.GetContent()
    ) { uri: Uri? ->
        if (uri != null) {
            uploadUriToVfs(
                context = context,
                uri = uri,
                onStart = { isFileUploading = true },
                onComplete = { result ->
                    isFileUploading = false
                    result.onSuccess {
                        val fileName = getFileName(context, uri) ?: "file_${System.currentTimeMillis()}"
                        onValueChange(fileName)
                        context.toast("File '$fileName' saved to VFS successfully")
                    }
                    result.onFailure { err ->
                        context.toast("VFS upload failed: ${err.message}", long = true)
                    }
                }
            )
        }
    }

    val cardBorderColor = if (errorMessage != null) Color(0xFFFF5252) else Color.DarkGray

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(CardGray, shape = RoundedCornerShape(10.dp))
            .border(1.dp, cardBorderColor, shape = RoundedCornerShape(10.dp))
            .padding(12.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(
                text = "${param.name} (${param.type})",
                color = Color.White,
                fontWeight = FontWeight.Bold,
                fontSize = 14.sp
            )
            Surface(
                color = if (param.required) Color(0xFFFF5252).copy(alpha = 0.2f) else Color.Gray.copy(alpha = 0.2f),
                shape = RoundedCornerShape(4.dp)
            ) {
                Text(
                    text = if (param.required) "REQUERIDO" else "OPCIONAL",
                    color = if (param.required) Color(0xFFFF5252) else Color.LightGray,
                    fontSize = 10.sp,
                    fontWeight = FontWeight.Bold,
                    modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                )
            }
        }

        if (param.description.isNotEmpty()) {
            Spacer(Modifier.height(4.dp))
            Text(param.description, color = Color.Gray, fontSize = 12.sp)
        }

        Spacer(Modifier.height(8.dp))

        when (param.type) {
            "bool" -> {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Switch(
                        checked = boolValue,
                        onCheckedChange = {
                            onValueChange(it)
                        }
                    )
                    Spacer(Modifier.width(8.dp))
                    Text(if (boolValue) "True" else "False", color = Color.White)
                }
            }
            "int" -> {
                OutlinedTextField(
                    value = stringValue,
                    onValueChange = {
                        onValueChange(it.toIntOrNull() ?: it)
                    },
                    isError = errorMessage != null,
                    modifier = Modifier.fillMaxWidth(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    singleLine = true
                )
            }
            "float", "double" -> {
                OutlinedTextField(
                    value = stringValue,
                    onValueChange = {
                        onValueChange(it.toDoubleOrNull() ?: it)
                    },
                    isError = errorMessage != null,
                    modifier = Modifier.fillMaxWidth(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
                    singleLine = true
                )
            }
            else -> {
                if (isFilePicker) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        OutlinedTextField(
                            value = stringValue,
                            onValueChange = { onValueChange(it) },
                            readOnly = true,
                            isError = errorMessage != null,
                            modifier = Modifier.weight(1f),
                            placeholder = { Text(if (isImagePicker) "No image selected" else "No file selected") }
                        )
                        Spacer(Modifier.width(8.dp))
                        Button(
                            onClick = { 
                                val typeFilter = if (isImagePicker) "image/*" else "*/*"
                                filePickerLauncher.launch(typeFilter) 
                            },
                            colors = ButtonDefaults.buttonColors(containerColor = VioletPrimary),
                            enabled = !isFileUploading
                        ) {
                            if (isFileUploading) {
                                CircularProgressIndicator(color = Color.White, modifier = Modifier.size(18.dp))
                            } else {
                                Text("Pick")
                            }
                        }
                    }
                } else {
                    val isPassword = param.uiHint == "password"
                    OutlinedTextField(
                        value = stringValue,
                        onValueChange = { onValueChange(it) },
                        isError = errorMessage != null,
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true,
                        visualTransformation = if (isPassword) PasswordVisualTransformation() else VisualTransformation.None
                    )
                }
            }
        }

        if (errorMessage != null) {
            Spacer(Modifier.height(4.dp))
            Text(errorMessage, color = Color(0xFFFF5252), fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
        }
    }
}

@Composable
fun DynamicActionCard(
    actionName: String,
    title: String,
    description: String?,
    parameters: List<FormParameter>,
    submitButtonText: String,
    onSubmit: (inputs: Map<String, Any>, onComplete: (Result<String>) -> Unit) -> Unit
) {
    ProxymaCard {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(title, fontWeight = FontWeight.Bold, fontSize = 18.sp, color = Color.White)
            if (!description.isNullOrEmpty()) {
                Spacer(Modifier.height(4.dp))
                Text(description, color = Color.Gray, fontSize = 13.sp)
            }
            Spacer(Modifier.height(12.dp))

            DynamicActionForm(
                parameters = parameters,
                submitButtonText = submitButtonText,
                onSubmit = onSubmit
            )
        }
    }
}
