package com.proxyma.android.models

data class Peer(val id: String, val address: String, val online: Boolean)

data class VfsFile(
    val name: String,
    val version: Int,
    val size: Long,
    val hash: String,
    val subscribed: Boolean,
    val hasLocal: Boolean,
    val deleted: Boolean,
    val upSpeed: Double,
    val downSpeed: Double
)

data class LogRecord(val timestamp: String, val level: String, val message: String)

data class ParameterDetail(
    val name: String,
    val type: String,
    val required: Boolean,
    val description: String
)

data class ServiceDetail(
    val name: String,
    val description: String,
    val providerAddress: String,
    val requiredPermissions: List<String>,
    val parameters: List<ParameterDetail>,
    val error: String? = null
)
