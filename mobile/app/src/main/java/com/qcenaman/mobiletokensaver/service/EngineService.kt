package com.qcenaman.mobiletokensaver.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.R
import com.qcenaman.mobiletokensaver.net.HttpServer
import com.qcenaman.mobiletokensaver.engine.Keeper
import com.qcenaman.mobiletokensaver.net.PcReporter
import com.qcenaman.mobiletokensaver.net.Tunnel

/** Foreground service that keeps the engine reachable: persistent notification, wake lock, HTTP server, link keeper. */
class EngineService : Service() {
    private var http: HttpServer? = null
    private var wake: PowerManager.WakeLock? = null
    private val handler = Handler(Looper.getMainLooper())
    /** Re-acquires the wake lock with a timeout while the engine runs, so a crash can never leave it held forever. */
    private val renewWake = object : Runnable {
        override fun run() {
            wake?.acquire(WAKE_MS)
            handler.postDelayed(this, WAKE_MS - 60_000L)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (Prefs.killed) { stopSelf(); return START_NOT_STICKY }
        startForeground(NOTIF_ID, buildNotification())
        ensureAccessibilityEnabled()
        if (Prefs.stayAwake && wake == null) {
            val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
            wake = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "MobileTokenSaver:engine").apply { setReferenceCounted(false) }
            handler.post(renewWake)
        }
        if (http == null) {
            http = HttpServer(Prefs.port).also { try { it.start(SOCKET_TIMEOUT_MS, false); CmdLog.add("http server on :${Prefs.port}") } catch (e: Exception) { CmdLog.add("http start failed: ${e.message}") } }
        }
        Keeper.start(this)
        PcReporter.start(this)
        if (Prefs.tunnelOn) Tunnel.start(this)
        return START_STICKY
    }

    override fun onDestroy() {
        http?.stop(); http = null
        Keeper.stop()
        PcReporter.stop()
        Tunnel.stop()
        handler.removeCallbacks(renewWake)
        wake?.let { if (it.isHeld) it.release() }; wake = null
        super.onDestroy()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        // Relaunch ourselves if the OS tries to drop us when the task is swiped away.
        if (!Prefs.killed) {
            val restart = Intent(applicationContext, EngineService::class.java)
            val pi = PendingIntent.getService(this, 1, restart, PendingIntent.FLAG_ONE_SHOT or PendingIntent.FLAG_IMMUTABLE)
            (getSystemService(Context.ALARM_SERVICE) as android.app.AlarmManager)
                .set(android.app.AlarmManager.RTC, System.currentTimeMillis() + 1000, pi)
        }
        super.onTaskRemoved(rootIntent)
    }

    /** Updates switch our accessibility service off; with WRITE_SECURE_SETTINGS we switch it back on ourselves. */
    private fun ensureAccessibilityEnabled() {
        try {
            val me = "$packageName/${A11yService::class.java.name}"
            val cr = contentResolver
            val cur = android.provider.Settings.Secure.getString(cr, "enabled_accessibility_services") ?: ""
            if (cur.split(':').none { it.equals(me, true) }) {
                android.provider.Settings.Secure.putString(cr, "enabled_accessibility_services", if (cur.isBlank()) me else "$cur:$me")
                android.provider.Settings.Secure.putInt(cr, "accessibility_enabled", 1)
                CmdLog.add("accessibility service re-enabled")
            }
        } catch (e: Exception) { CmdLog.add("cannot enable accessibility myself: ${e.message}") }
    }

    private fun buildNotification(): Notification {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(
            NotificationChannel(CH, "MobileTokenSaver engine", NotificationManager.IMPORTANCE_LOW).apply {
                setShowBadge(false); description = "Keeps the phone engine reachable."
            })
        fun action(label: String, what: String): Notification.Action {
            val pi = PendingIntent.getBroadcast(this, what.hashCode(),
                Intent(this, ActionReceiver::class.java).setAction(what),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
            return Notification.Action.Builder(null, label, pi).build()
        }
        val state = when { Prefs.paused -> "Paused — Claude's tool calls are refused"; else -> "Running" }
        val b = Notification.Builder(this, CH)
        return b.setContentTitle("MobileTokenSaver")
            .setContentText(state)
            .setSmallIcon(R.drawable.ic_engine)
            .setOngoing(true)
            .addAction(action(if (Prefs.paused) "Resume" else "Pause", ActionReceiver.PAUSE))
            .addAction(action("Kill", ActionReceiver.KILL))
            .build()
    }

    companion object {
        const val CH = "engine"; const val NOTIF_ID = 42
        private const val WAKE_MS = 10 * 60_000L
        private const val SOCKET_TIMEOUT_MS = 60_000
        fun start(c: Context) {
            if (Prefs.killed) return
            val i = Intent(c, EngineService::class.java)
            c.startForegroundService(i)
        }
        fun refresh(c: Context) = start(c)
    }
}
