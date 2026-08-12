package com.proxyma.android.models

import androidx.compose.runtime.State
import androidx.compose.runtime.mutableStateOf

class ObservableBindingState<T> {
    private val mutableState = mutableStateOf<T?>(null)

    val state: State<T?>
        get() = mutableState

    fun bind(value: T) {
        mutableState.value = value
    }

    fun unbind() {
        mutableState.value = null
    }
}

data class Peer(
    val id: String = "",
    val address: String = "",
    val online: Boolean = false
)

data class VfsFile(
    val name: String = "",
    val version: Int = 0,
    val size: Long = 0,
    val hash: String = "",
    val subscribed: Boolean = false,
    val hasLocal: Boolean = false,
    val deleted: Boolean = false,
    val upSpeed: Double = 0.0,
    val downSpeed: Double = 0.0
)

data class LogRecord(
    val timestamp: String = "",
    val level: String = "",
    val message: String = ""
)

data class FormParameter(
    val name: String = "",
    val type: String = "string",
    val required: Boolean = false,
    val description: String = "",
    val uiHint: String? = null,
    val defaultValue: String? = null,
    val options: List<String> = emptyList()
) {
    fun isFilePicker(): Boolean =
        type == "file" || uiHint == "file_picker" || uiHint == "image_picker" || uiHint == "audio_picker"

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
    val name: String = "",
    val description: String? = null,
    val isStreaming: Boolean? = false,
    val providerAddress: String? = null,
    val requiredPermissions: List<String> = emptyList(),
    val parameters: List<FormParameter> = emptyList(),
    val outputs: Map<String, ServiceParameter> = emptyMap(),
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
    val streamOutput: String? = null,
    val streamId: String? = null
)

class TaskLedger(private val tasks: MutableList<FileTask>) {
    fun addFirst(task: FileTask) {
        tasks.add(0, task)
    }

    fun remove(task: FileTask) {
        tasks.remove(task)
    }

    fun update(taskId: String, transform: (FileTask) -> FileTask) {
        val index = tasks.indexOfFirst { it.taskId == taskId }
        if (index >= 0) {
            tasks[index] = transform(tasks[index])
        }
    }
}

data class PipelineStep(
    val id: String = "",
    val service: String = "",
    val target_node_id: String? = null
)

data class PipelineConnection(
    val from_step: String = "",
    val from_port: String = "",
    val to_step: String = "",
    val to_port: String = ""
)

data class PipelineSchema(
    val id: String = "",
    val version: Int = 1,
    val steps: List<PipelineStep> = emptyList(),
    val connections: List<PipelineConnection> = emptyList()
)

data class ServiceParameter(
    val type: String = "string",
    val required: Boolean = false,
    val default: String? = null,
    val options: List<String> = emptyList(),
    val uiHint: String? = null
)

data class RunDialogTarget(
    val name: String? = null,
    val isPipeline: Boolean = false,
    val isStreaming: Boolean = false,
    val specs: List<FormParameter>? = null
) {
    val isVisible: Boolean
        get() = name != null && specs != null

    fun reset(): RunDialogTarget = RunDialogTarget()
}
