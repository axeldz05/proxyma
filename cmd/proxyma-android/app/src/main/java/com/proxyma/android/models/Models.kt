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

data class FormParameter(
    val name: String,
    val type: String,
    val required: Boolean,
    val description: String,
    val uiHint: String? = null,
    val defaultValue: String? = null,
    val options: List<String>? = null
) {
    fun isFilePicker(): Boolean =
        type == "file" || uiHint == "file_picker" || uiHint == "image_picker"

    fun isImagePicker(): Boolean = uiHint == "image_picker"
}

data class ServiceUIConfig(
    val type: String? = null,
    val vfs_path: String? = null,
    val local_path: String? = null,
    val url: String? = null,
    val widget_type: String? = null
)

data class ServiceDetail(
    val name: String,
    val description: String?,
    val isStreaming: Boolean? = false,
    val providerAddress: String?,
    val requiredPermissions: List<String>?,
    val parameters: List<FormParameter>?,
    val outputs: Map<String, ServiceParameter>? = null,
    val ui: ServiceUIConfig? = null,
    val error: String? = null
)

data class FileTask(
    val taskId: String,
    val service: String,
    val input: String,
    val output: String,
    val status: String, // "running", "streaming", "completed", "failed"
    val resultPath: String? = null,
    val error: String? = null,
    val isStreaming: Boolean = false,
    val streamOutput: String? = null
)

data class PipelineStep(
    val id: String,
    val service: String,
    val target_node_id: String? = null
)

data class PipelineConnection(
    val from_step: String,
    val from_port: String,
    val to_step: String,
    val to_port: String
)

data class PipelineSchema(
    val id: String,
    val version: Int,
    val steps: List<PipelineStep>,
    val connections: List<PipelineConnection>
)

data class ServiceParameter(
    val type: String,
    val required: Boolean = false,
    val default: String? = null,
    val options: List<String>? = null,
    val uiHint: String? = null
)

data class ServiceSchema(
    val name: String,
    val description: String? = null,
    val parameters: Map<String, ServiceParameter>? = null,
    val outputs: Map<String, ServiceParameter>? = null
)


