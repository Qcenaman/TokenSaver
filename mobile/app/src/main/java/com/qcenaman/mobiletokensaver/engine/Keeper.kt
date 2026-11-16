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
    private const val EVERY_MS = 5_000L // short tick; repair() spaces the real attempts out with backoff
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
        // Any network change (Wi-Fi ↔ Ethernet, a new eth0, VPN): the ADB connection may point at a dead address and the
        // helper may have gone with adbd, so re-check the shell at once.
        runCatching {
            cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(n: Network) { netChanged(app, "network up") }
                override fun onLost(n: Network) { netChanged(app, "network lost") }
                override fun onLinkPropertiesChanged(n: Network, lp: android.net.LinkProperties) { netChanged(app, "addresses changed") }
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

    @Volatile private var lastNet = 0L

    private fun netChanged(ctx: Context, why: String) {
        val now = System.currentTimeMillis()
        if (now - lastNet < 3_000) return // one burst of callbacks = one re-check
        lastNet = now
        CmdLog.add("shell re-check: $why")
        if (!HelperClient.alive()) runCatching { AdbManager.get(ctx).dropConnection() }
        kick(ctx)
    }

    /** Wake the loop now instead of at its next tick. */
    fun kick(ctx: Context) { kicked = true; synchronized(lock) { lock.notifyAll() } }
    @Volatile private var kicked = false

    @Volatile private var lastShizukuAsk = 0L
    @Volatile private var lastGood = 0L
    @Volatile private var backoffMs = 0L
    @Volatile private var nextTry = 0L

    private fun repair(ctx: Context) {
        val t0 = System.currentTimeMillis()
        if (t0 - lastGood < 30_000 && HelperClient.alive()) return // checked by a real command less than 30 s ago
        if (HelperClient.works()) { backoffMs = 0L; lastGood = t0; return }
        // retry with backoff while nothing works (a kick from a grant or network change skips the wait)
        val now = System.currentTimeMillis()
        if (now < nextTry && !kicked) return
        kicked = false
        backoffMs = if (backoffMs == 0L) 5_000L else minOf(backoffMs * 2, 5 * 60_000L)
        nextTry = now + backoffMs
        if (HelperClient.alive()) { if (HelperClient.relaunch(ctx, reason = "watchdog: it answers but cannot run commands")) { backoffMs = 0L; return } }
        else if (HelperClient.ensureStarted(ctx)) { backoffMs = 0L; return }
        // Shizuku running but not allowed: ask, and if the dialog is not answered, accept it on screen (screen-off only).
        if (Privileged.shizukuState() == Privileged.ShizukuState.NO_PERMISSION) {
            Privileged.requestShizuku()
            if (now - lastShizukuAsk > 20 * 60_000L) {
                lastShizukuAsk = now
                if (UiAdbSetup.acceptShizuku(ctx) && HelperClient.ensureStarted(ctx)) { backoffMs = 0L; return }
            }
        }
        if (Privileged.suPath() != null) return
        if (!wifiUp(ctx)) return
        enableWirelessDebugging(ctx)
        // Connecting once over Wi-Fi also switches adbd to loopback TCP, so later starts need no Wi-Fi.
        if (!AdbManager.get(ctx).needsPairing && HelperClient.ensureStarted(ctx)) return
        // Nothing privileged could flip the switch, or our key is not paired: drive Settings on screen (a11y + OCR).
        if (!UiAdbSetup.wirelessOn(ctx) || AdbManager.get(ctx).needsPairing) {
            val r = UiAdbSetup.autoRun(ctx)
            if (r?.ok == true && HelperClient.ensureStarted(ctx)) return
        }
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
