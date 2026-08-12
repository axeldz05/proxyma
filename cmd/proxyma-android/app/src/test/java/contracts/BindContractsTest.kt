package contracts

import com.proxyma.android.utils.TaskResultReference
import com.proxyma.android.utils.BindMethod
import com.proxyma.android.utils.ReflectiveBindStreamApi
import com.proxyma.android.utils.ReflectiveNodeStopApi
import com.proxyma.android.utils.StopBindingMode
import com.proxyma.android.utils.bindResult
import com.proxyma.android.utils.consumePrepared
import com.proxyma.android.utils.isBindError
import com.proxyma.android.utils.launchManagedBindStream
import com.proxyma.android.utils.normalizeVfsUploadResult
import com.proxyma.android.utils.parseBindError
import com.proxyma.android.utils.parseTaskResultReference
import com.proxyma.android.utils.requireContentStream
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.assertThrows
import org.junit.Test

class BindContractsTest {
    @Test
    fun errorEnvelopeIsParsedOnce() {
        val response = """{"error":"disk failed: no space"}"""

        assertTrue(isBindError(response))
        assertEquals("disk failed: no space", parseBindError(response))
    }

    @Test
    fun emptyErrorEnvelopeIsSuccess() {
        val response = """{"error":""}"""

        assertFalse(isBindError(response))
        assertTrue(bindResult(response).isSuccess)
    }

    @Test
    fun whitespaceErrorEnvelopeIsSuccess() {
        val response = """{"error":"   "}"""

        assertFalse(isBindError(response))
        assertTrue(bindResult(response).isSuccess)
    }

    @Test
    fun successPayloadIsNotMisclassifiedAsError() {
        val response = """{"message":"file fetched"}"""

        assertFalse(isBindError(response))
        assertEquals(response, bindResult(response).getOrThrow())
    }

    @Test
    fun nonEnvelopePayloadRemainsSuccessful() {
        val response = "/data/user/0/com.proxyma.android/files/blob"

        assertFalse(isBindError(response))
        assertEquals(response, bindResult(response).getOrThrow())
    }

    @Test
    fun legacyErrorPrefixIsMethodSpecific() {
        val response = "error: peer unavailable"

        assertTrue(bindResult(response).isSuccess)
        assertEquals(
            "peer unavailable",
            bindResult(response, BindMethod.LEGACY_ERROR_PREFIX)
                .exceptionOrNull()
                ?.message
        )
    }

    @Test
    fun startNodeTreatsLegacyRawTextAsError() {
        assertEquals(
            "failed to load config",
            bindResult("failed to load config", BindMethod.START_NODE)
                .exceptionOrNull()
                ?.message
        )
        assertTrue(bindResult(" \n ", BindMethod.START_NODE).isSuccess)
        assertTrue(bindResult("""{"error":""}""", BindMethod.START_NODE).isSuccess)
    }

    @Test
    fun nestedTaskResultFindsLocalPath() {
        val response = """{"data":{"outputs":{"result_path":"/tmp/result.pdf"}}}"""

        assertEquals(
            TaskResultReference(localPath = "/tmp/result.pdf"),
            parseTaskResultReference(response)
        )
    }

    @Test
    fun taskResultFallsBackToVfsHash() {
        val response = """{"outputs":{"document":"vfs://abc123"}}"""

        assertEquals(
            TaskResultReference(blobHash = "abc123"),
            parseTaskResultReference(response)
        )
    }

    @Test
    fun uploadResultKeepsLogicalNameAndSeparateMessage() {
        val result = normalizeVfsUploadResult(
            logicalName = "camera.jpg",
            response = """{"message":"Uploaded to CAS"}"""
        ).getOrThrow()

        assertEquals("camera.jpg", result.logicalName)
        assertEquals("Uploaded to CAS", result.message)
    }

    @Test
    fun nullContentStreamThrows() {
        val error = assertThrows(IOException::class.java) {
            requireContentStream(null, "content://missing")
        }

        assertTrue(error.message.orEmpty().contains("content://missing"))
    }

    @Test
    fun preparedLaunchFailureStaysInResult() {
        val result = consumePrepared(Result.success("intent")) {
            throw IllegalStateException("no viewer")
        }

        assertEquals("no viewer", result.exceptionOrNull()?.message)
    }

    @Test
    fun modernStreamFixtureReturnsIdAndCancelsOnce() {
        val fixture = ModernStreamFixture()
        val api = ReflectiveBindStreamApi(fixture.javaClass, fixture)

        val lease = api.start("events", "{}", Any()).getOrThrow()
        assertEquals("stream-fixture", lease.streamId)
        assertEquals(1, fixture.starts)

        lease.cancel().getOrThrow()
        lease.cancel().getOrThrow()
        assertEquals("stream-fixture", fixture.canceledId)
        assertEquals(1, fixture.cancels)
    }

    @Test
    fun staleStreamFixtureIsRejectedBeforeStarting() {
        val fixture = LegacyStreamFixture()
        val api = ReflectiveBindStreamApi(fixture.javaClass, fixture)

        val result = api.start("events", "{}", Any())

        assertTrue(result.isFailure)
        assertEquals(0, fixture.starts)
        assertNull(result.getOrNull())
    }

    @Test
    fun managedStreamCancelsLeaseWithOwningScope() = runBlocking {
        val fixture = ModernStreamFixture()
        val api = ReflectiveBindStreamApi(fixture.javaClass, fixture)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val started = CompletableDeferred<String>()

        val job = launchManagedBindStream(
            scope = scope,
            serviceName = "events",
            payloadJson = "{}",
            listenerFactory = { Any() },
            onStarted = { started.complete(it) },
            api = api,
            callbackDispatcher = Dispatchers.Unconfined
        )

        assertEquals("stream-fixture", withTimeout(2_000) { started.await() })
        scope.cancel()
        withTimeout(2_000) { job.join() }
        assertEquals(1, fixture.cancels)
    }

    @Test
    fun modernNodeStopUsesErrorReportingApi() {
        val fixture = ModernNodeStopFixture()
        val api = ReflectiveNodeStopApi(fixture.javaClass, fixture)

        val result = api.stop().getOrThrow()

        assertEquals(StopBindingMode.STOP_NODE_WITH_ERROR, result.mode)
        assertEquals(1, fixture.modernStops)
        assertEquals(0, fixture.legacyStops)
    }

    @Test
    fun modernNodeStopFailureDoesNotFallBack() {
        val fixture = ModernNodeStopFixture(
            response = """{"error":"shutdown deadline exceeded"}"""
        )
        val api = ReflectiveNodeStopApi(fixture.javaClass, fixture)

        val result = api.stop()

        assertEquals("shutdown deadline exceeded", result.exceptionOrNull()?.message)
        assertEquals(1, fixture.modernStops)
        assertEquals(0, fixture.legacyStops)
    }

    @Test
    fun staleNodeBindingUsesExplicitLegacyFallback() {
        val fixture = LegacyNodeStopFixture()
        val api = ReflectiveNodeStopApi(fixture.javaClass, fixture)

        val result = api.stop().getOrThrow()

        assertEquals(StopBindingMode.LEGACY_STOP_NODE, result.mode)
        assertEquals(1, fixture.legacyStops)
    }

    private class ModernStreamFixture {
        var starts = 0
        var cancels = 0
        var canceledId: String? = null

        @Suppress("UNUSED_PARAMETER")
        fun streamService(name: String, payload: String, listener: Any): String {
            starts++
            return """{"status":"streaming_started","stream_id":"stream-fixture"}"""
        }

        fun cancelStream(streamId: String): String {
            cancels++
            canceledId = streamId
            return """{"message":"cancelled"}"""
        }
    }

    private class LegacyStreamFixture {
        var starts = 0

        @Suppress("UNUSED_PARAMETER")
        fun streamService(name: String, payload: String, listener: Any) {
            starts++
        }
    }

    private class ModernNodeStopFixture(
        private val response: String = ""
    ) {
        var modernStops = 0
        var legacyStops = 0

        fun stopNodeWithError(): String {
            modernStops++
            return response
        }

        fun stopNode() {
            legacyStops++
        }
    }

    private class LegacyNodeStopFixture {
        var legacyStops = 0

        fun stopNode() {
            legacyStops++
        }
    }
}
