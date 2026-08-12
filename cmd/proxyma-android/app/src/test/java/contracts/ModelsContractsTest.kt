package contracts

import com.google.gson.Gson
import com.proxyma.android.models.FormParameter
import com.proxyma.android.models.FileTask
import com.proxyma.android.models.PipelineSchema
import com.proxyma.android.models.ObservableBindingState
import com.proxyma.android.models.RunDialogTarget
import com.proxyma.android.models.ServiceDetail
import com.proxyma.android.models.TaskLedger
import com.proxyma.android.utils.DaemonState
import com.proxyma.android.utils.DaemonStateMachine
import com.proxyma.android.utils.StopRequest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ModelsContractsTest {
    private val gson = Gson()

    @Test
    fun oneStepPipelineDefaultsMissingConnections() {
        val schema = gson.fromJson(
            """{"id":"single","version":1,"steps":[{"id":"only","service":"echo"}]}""",
            PipelineSchema::class.java
        )

        assertEquals(1, schema.steps.size)
        assertTrue(schema.connections.isEmpty())
    }

    @Test
    fun omittedServiceCollectionsAreEmpty() {
        val detail = gson.fromJson(
            """{"name":"echo","description":"Echo"}""",
            ServiceDetail::class.java
        )

        assertTrue(detail.requiredPermissions.isEmpty())
        assertTrue(detail.parameters.isEmpty())
        assertTrue(detail.outputs.isEmpty())
    }

    @Test
    fun omittedParameterOptionsAreEmpty() {
        val parameter = gson.fromJson(
            """{"name":"input","type":"string"}""",
            FormParameter::class.java
        )

        assertTrue(parameter.options.isEmpty())
    }

    @Test
    fun resettingRunTargetClearsEveryModeFlag() {
        val active = RunDialogTarget(
            name = "stream-pipeline",
            isPipeline = true,
            isStreaming = true,
            specs = listOf(FormParameter(name = "input"))
        )

        val reset = active.reset()

        assertEquals(null, reset.name)
        assertFalse(reset.isPipeline)
        assertFalse(reset.isStreaming)
        assertEquals(null, reset.specs)
        assertFalse(reset.isVisible)
    }

    @Test
    fun daemonStateMachineSerializesStartAndStopIntent() {
        val state = DaemonStateMachine()

        assertTrue(state.requestStart())
        assertFalse(state.requestStart())
        assertEquals(DaemonState.STARTING, state.current)
        assertEquals(StopRequest.EXECUTE, state.requestStop())
        assertEquals(DaemonState.STOPPING, state.current)
        assertFalse(state.markStarted())
        state.markStartFailed()
        assertEquals(DaemonState.STOPPING, state.current)
        state.markStopped()
        assertEquals(DaemonState.STOPPED, state.current)
        assertEquals(StopRequest.ALREADY_STOPPED, state.requestStop())
    }

    @Test
    fun failedStopRemainsErrorUntilSuccessfulRetry() {
        val state = DaemonStateMachine()
        assertTrue(state.requestStart())
        assertTrue(state.markStarted())
        assertEquals(StopRequest.EXECUTE, state.requestStop())

        state.markStopFailed()
        assertEquals(DaemonState.ERROR, state.current)
        assertEquals(StopRequest.EXECUTE, state.requestStop())
        assertEquals(DaemonState.STOPPING, state.current)

        state.markStopped()
        assertEquals(DaemonState.STOPPED, state.current)
    }

    @Test
    fun observableBindingPublishesConnectAndDisconnect() {
        val binding = ObservableBindingState<String>()

        assertEquals(null, binding.state.value)
        binding.bind("service")
        assertEquals("service", binding.state.value)
        binding.unbind()
        assertEquals(null, binding.state.value)
    }

    @Test
    fun taskLedgerKeepsTaskStateOutsideComposableOwnership() {
        val tasks = mutableListOf<FileTask>()
        val ledger = TaskLedger(tasks)
        val task = FileTask(
            taskId = "task-1",
            service = "events",
            input = "{}",
            output = "stream",
            status = "streaming"
        )

        ledger.addFirst(task)
        ledger.update("task-1") {
            it.copy(status = "completed", streamId = "stream-1")
        }

        assertEquals(1, tasks.size)
        assertEquals("completed", tasks.single().status)
        assertEquals("stream-1", tasks.single().streamId)
    }
}
