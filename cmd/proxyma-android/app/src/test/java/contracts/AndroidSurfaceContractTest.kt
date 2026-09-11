package contracts

import android.app.Application
import android.content.Intent
import android.net.Uri
import android.provider.MediaStore
import android.webkit.WebView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.test.core.app.ApplicationProvider
import com.proxyma.android.ui.screens.ProxymaJSBridge
import com.proxyma.android.ui.screens.ServiceWebBindGateway
import com.proxyma.android.utils.TaskResultReference
import com.proxyma.android.utils.parseTaskResultReference
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class AndroidSurfaceContractTest {
    private val bridges = mutableListOf<ProxymaJSBridge>()

    @After
    fun closeBridges() {
        bridges.forEach(ProxymaJSBridge::close)
    }

    @Test
    fun delegatedWebBridgeRunsServiceAndPreservesResultReference() {
        val gateway = FakeServiceWebGateway(
            runResponse = """{"status":"completed","outputs":{"document":"vfs://abc123"}}"""
        )
        val bridge = newBridge(gateway)

        val response = bridge.runService("documents.render", """{"name":"report"}""")

        assertEquals("documents.render", gateway.lastService)
        assertEquals("""{"name":"report"}""", gateway.lastPayload)
        assertEquals(
            TaskResultReference(blobHash = "abc123"),
            parseTaskResultReference(response)
        )
    }

    @Test
    fun delegatedWebBridgeNormalizesBindingFailureForJavascript() {
        val bridge = newBridge(FakeServiceWebGateway(runResponse = "error: provider unavailable"))

        val response = JSONObject(bridge.runService("documents.render", "{}"))

        assertEquals("provider unavailable", response.getString("error"))
    }

    @Test
    fun nativeFileAndCameraPickersExposeExpectedAndroidIntents() {
        val context = ApplicationProvider.getApplicationContext<Application>()
        val contentIntent = ActivityResultContracts.GetContent().createIntent(context, "image/*")

        assertEquals(Intent.ACTION_GET_CONTENT, contentIntent.action)
        assertEquals("image/*", contentIntent.type)
        assertTrue(contentIntent.categories.orEmpty().contains(Intent.CATEGORY_OPENABLE))

        val output = Uri.parse("content://proxyma.test/camera.jpg")
        val cameraIntent = ActivityResultContracts.TakePicture().createIntent(context, output)
        assertEquals(MediaStore.ACTION_IMAGE_CAPTURE, cameraIntent.action)
        assertEquals(
            output,
            cameraIntent.getParcelableExtra(MediaStore.EXTRA_OUTPUT, Uri::class.java)
        )
    }

    private fun newBridge(gateway: ServiceWebBindGateway): ProxymaJSBridge {
        val context = ApplicationProvider.getApplicationContext<Application>()
        return ProxymaJSBridge(WebView(context), gateway).also(bridges::add)
    }

    private class FakeServiceWebGateway(
        private val runResponse: String,
        private val uiContent: String = "<html>fixture</html>"
    ) : ServiceWebBindGateway {
        var lastService: String? = null
        var lastPayload: String? = null

        override fun runService(name: String, payloadJson: String): String {
            lastService = name
            lastPayload = payloadJson
            return runResponse
        }

        override fun getServiceUIContent(name: String): String = uiContent
    }
}
