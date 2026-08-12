package com.proxyma.android

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Binder
import android.os.Build
import android.os.IBinder
import androidx.annotation.MainThread
import androidx.core.app.NotificationCompat
import com.proxyma.android.utils.BindMethod
import com.proxyma.android.utils.DaemonState
import com.proxyma.android.utils.DaemonStateMachine
import com.proxyma.android.utils.ReflectiveNodeStopApi
import com.proxyma.android.utils.StopBindingMode
import com.proxyma.android.utils.StopRequest
import com.proxyma.android.utils.bindResult
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

class ProxymaService : Service() {

    private val binder = LocalBinder()
    private val serviceJob = SupervisorJob()
    private val serviceScope = CoroutineScope(serviceJob + Dispatchers.IO)
    private val lifecycleMutex = Mutex()
    private val daemonState = DaemonStateMachine()
    private val stopCallbacks = mutableListOf<() -> Unit>()
    private val stopFailureCallbacks = mutableListOf<() -> Unit>()
    private val nodeStopApi by lazy {
        ReflectiveNodeStopApi(proxyma_bind.Proxyma_bind::class.java)
    }
    private var storagePath: String = ""

    inner class LocalBinder : Binder() {
        fun getService(): ProxymaService = this@ProxymaService
    }

    override fun onBind(intent: Intent?): IBinder {
        return binder
    }

    override fun onCreate() {
        super.onCreate()
        storagePath = File(filesDir, "proxyma_data").absolutePath
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action
        if (action == ACTION_STOP) {
            stopDaemon(onStopped = ::stopSelf)
            return START_NOT_STICKY
        }

        startDaemon()
        return START_STICKY
    }

    @MainThread
    fun startDaemon() {
        if (!daemonState.requestStart()) return

        // Show foreground notification
        startForeground(NOTIFICATION_ID, buildNotification("Starting Node..."))

        serviceScope.launch {
            lifecycleMutex.withLock {
                try {
                    val dir = File(storagePath)
                    if (!dir.exists() && !dir.mkdirs()) {
                        throw IllegalStateException("Unable to create storage directory")
                    }
                    proxyma_bind.Proxyma_bind.setStoragePath(storagePath)
                    bindResult(
                        proxyma_bind.Proxyma_bind.startNode(storagePath, true),
                        BindMethod.START_NODE
                    ).getOrThrow()
                    if (daemonState.markStarted()) {
                        val nodeId = proxyma_bind.Proxyma_bind.getNodeID()
                        updateNotification("Node Online (ID: $nodeId)")
                    }
                } catch (error: Exception) {
                    daemonState.markStartFailed()
                    updateNotification("Error: ${error.message}")
                }
            }
        }
    }

    @Synchronized
    @MainThread
    fun stopDaemon(
        onStopped: () -> Unit = {},
        onFailure: () -> Unit = {}
    ) {
        when (daemonState.requestStop()) {
            StopRequest.ALREADY_STOPPED -> {
                removeForeground()
                onStopped()
            }
            StopRequest.WAIT -> {
                stopCallbacks += onStopped
                stopFailureCallbacks += onFailure
            }
            StopRequest.EXECUTE -> serviceScope.launch {
                synchronized(this@ProxymaService) {
                    stopCallbacks += onStopped
                    stopFailureCallbacks += onFailure
                }
                lifecycleMutex.withLock {
                    try {
                        val stopResult = nodeStopApi.stop().getOrThrow()
                        if (stopResult.mode == StopBindingMode.LEGACY_STOP_NODE) {
                            android.util.Log.w(
                                "ProxymaService",
                                "Bundled proxyma.aar lacks StopNodeWithError; used legacy StopNode"
                            )
                        }
                        daemonState.markStopped()
                        withContext(Dispatchers.Main.immediate) {
                            removeForeground()
                            drainStopCallbacks(success = true).forEach { callback ->
                                callback()
                            }
                        }
                    } catch (error: Exception) {
                        daemonState.markStopFailed()
                        withContext(Dispatchers.Main.immediate) {
                            updateNotification("Stop failed: ${error.message}")
                            drainStopCallbacks(success = false).forEach { callback ->
                                callback()
                            }
                        }
                    }
                }
            }
        }
    }

    @Synchronized
    private fun drainStopCallbacks(success: Boolean): List<() -> Unit> {
        val callbacks = if (success) {
            stopCallbacks.toList()
        } else {
            stopFailureCallbacks.toList()
        }
        stopCallbacks.clear()
        stopFailureCallbacks.clear()
        return callbacks
    }

    private fun removeForeground() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }

    fun getStoragePath(): String {
        return storagePath
    }

    fun setStoragePath(newPath: String) {
        this.storagePath = newPath
    }

    private fun buildNotification(text: String): Notification {
        val stopIntent = Intent(this, ProxymaService::class.java).apply {
            action = ACTION_STOP
        }
        val stopPendingIntent = PendingIntent.getService(
            this,
            0,
            stopIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val mainIntent = Intent(this, MainActivity::class.java)
        val mainPendingIntent = PendingIntent.getActivity(
            this,
            0,
            mainIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        return NotificationCompat.Builder(this, ProxymaApp.CHANNEL_ID)
            .setContentTitle("Proxyma Node Daemon")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentIntent(mainPendingIntent)
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, "Stop Node", stopPendingIntent)
            .setOngoing(true)
            .build()
    }

    private fun updateNotification(text: String) {
        val notificationManager = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
        notificationManager.notify(NOTIFICATION_ID, buildNotification(text))
    }

    override fun onDestroy() {
        if (daemonState.current != DaemonState.STOPPED) {
            stopDaemon(
                onStopped = { serviceScope.cancel() },
                onFailure = { serviceScope.cancel() }
            )
        } else {
            serviceScope.cancel()
        }
        super.onDestroy()
    }

    companion object {
        const val NOTIFICATION_ID = 42
        const val ACTION_STOP = "com.proxyma.android.ACTION_STOP"
    }
}
