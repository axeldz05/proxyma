package com.proxyma.android

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Binder
import android.os.IBinder
import androidx.core.app.NotificationCompat
import java.io.File
import kotlin.concurrent.thread

class ProxymaService : Service() {

    private val binder = LocalBinder()
    private var isRunning = false
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
            stopDaemon()
            stopSelf()
            return START_NOT_STICKY
        }

        startDaemon()
        return START_STICKY
    }

    @Synchronized
    fun startDaemon() {
        if (isRunning) return
        isRunning = true

        // Create storage directory if it doesn't exist
        val dir = File(storagePath)
        if (!dir.exists()) {
            dir.mkdirs()
        }

        // Show foreground notification
        startForeground(NOTIFICATION_ID, buildNotification("Starting Node..."))

        thread(name = "ProxymaDaemonThread") {
            try {
                // Call gomobile bind method
                val err = proxyma_bind.Proxyma_bind.startNode(storagePath, true)
                if (err.isNotEmpty()) {
                    updateNotification("Error: $err")
                    isRunning = false
                } else {
                    val nodeId = proxyma_bind.Proxyma_bind.getNodeID()
                    updateNotification("Node Online (ID: $nodeId)")
                }
            } catch (e: Exception) {
                updateNotification("Exception: ${e.message}")
                isRunning = false
            }
        }
    }

    @Synchronized
    fun stopDaemon() {
        if (!isRunning) return
        isRunning = false
        thread {
            try {
                proxyma_bind.Proxyma_bind.stopNode()
            } catch (e: Exception) {
                e.printStackTrace()
            }
        }
        stopForeground(STOP_FOREGROUND_REMOVE)
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
        stopDaemon()
        super.onDestroy()
    }

    companion object {
        const val NOTIFICATION_ID = 42
        const val ACTION_STOP = "com.proxyma.android.ACTION_STOP"
    }
}
