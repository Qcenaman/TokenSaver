package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.provider.Settings
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.adb.AdbManager
import rikka.shizuku.Shizuku

/**
 * Keeps the shell helper running: retries every [EVERY_MS], as soon as Shizuku comes up or grants permission,
 * and when Wi-Fi returns (for the ADB fallback). Asks for an ADB pairing code only when nothing else can start it.
 */
object Keeper {
    private const val EVERY_MS = 30_000L
    @Volatile private var running = false
    @Volatile private var wifiUp = false
    @Volatile private var lastPairAsk = 0L
    private val lock = Object()

    fun start(ctx: Context) {
        if (running) return
        running = true
        val app = ctx.applicationContext
        val cm = app.getSystemService(ConnectivityManager::class.java)
        runCatching {
            cm.registerNetworkCallback(
                NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI).build(),
                object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(n: Network) { wifiUp = true; kick(app) }
                    override fun onLost(n: Network) { wifiUp = false }
                })
        }
        runCatching {
            Shizuku.addBinderReceivedListenerSticky { kick(app) }
            Shizuku.addRequestPermissionResultListener { _, r -> if (r == PackageManager.PERMISSION_GRANTED) { CmdLog.add("Shizuku permission granted"); kick(app) } }
        }
        Thread {
            while (running) {
                try { repair(app) } catch (_: Throwable) {}
                synchronized(lock) { try { lock.wait(EVERY_MS) } catch (_: InterruptedException) {} }
            }
        }.apply { isDaemon = true; name = "keeper" }.start()
    }

    /** Wake the loop now instead of at its next tick. */
    fun kick(ctx: Context) { synchronized(lock) { lock.notifyAll() } }

    private fun repair(ctx: Context) {
        if (HelperClient.alive()) return
        if (HelperClient.ensureStarted(ctx)) return
        if (Privileged.suPath() != null) return
        if (!wifiUp(ctx)) return
        enableWirelessDebugging(ctx)
        // Connecting once over Wi-Fi also switches adbd to loopback TCP, so later starts need no Wi-Fi.
        if (!AdbManager.get(ctx).needsPairing && HelperClient.ensureStarted(ctx)) return
        if (AdbManager.get(ctx).needsPairing && System.currentTimeMillis() - lastPairAsk > 30 * 60_000L) {
            lastPairAsk = System.currentTimeMillis()
            com.qcenaman.mobiletokensaver.adb.Pairing.startWatching(ctx)
            CmdLog.add("no Shizuku: pair once via Wireless debugging → Pair device with pairing code")
        }
    }

    /** Needs WRITE_SECURE_SETTINGS (the helper grants it to us the first time it runs). */
    private fun enableWirelessDebugging(ctx: Context) {
        try {
            val cr = ctx.contentResolver
            if (Settings.Global.getInt(cr, "adb_wifi_enabled", 0) != 1) {
                Settings.Global.putInt(cr, "adb_wifi_enabled", 1)
                CmdLog.add("wireless debugging switched on")
                Thread.sleep(1500)
            }
        } catch (_: Exception) {}
    }

    fun wifiUp(ctx: Context): Boolean = wifiUp || runCatching {
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        @Suppress("DEPRECATION")
        cm.allNetworks.any { n -> cm.getNetworkCapabilities(n)?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true }
    }.getOrDefault(false)

    fun stop() { running = false; synchronized(lock) { lock.notifyAll() } }
}
