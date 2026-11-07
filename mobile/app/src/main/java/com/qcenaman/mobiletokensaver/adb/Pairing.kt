package com.qcenaman.mobiletokensaver.adb

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.RemoteInput
import android.content.Context
import android.content.Intent
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.R
import com.qcenaman.mobiletokensaver.service.ActionReceiver
import kotlinx.coroutines.flow.MutableStateFlow
import kotlin.concurrent.thread

/**
 * One-time pairing with this phone's Wireless debugging.
 * Android closes the "Pair device with pairing code" dialog as soon as you leave Settings, so we
 * watch for the pairing service over mDNS and ask for the 6-digit code through a notification
 * with an inline reply — the user never has to leave the Settings screen.
 */
object Pairing {
    const val KEY_CODE = "code"
    const val NOTIF_ID = 43

    val status = MutableStateFlow("not paired")
    @Volatile private var pairPort = -1
    @Volatile private var listener: NsdManager.DiscoveryListener? = null

    fun startWatching(ctx: Context) {
        if (listener != null) return
        val nsd = ctx.getSystemService(Context.NSD_SERVICE) as NsdManager
        val l = object : NsdManager.DiscoveryListener {
            override fun onStartDiscoveryFailed(t: String?, e: Int) { status.value = "discovery failed ($e)" }
            override fun onStopDiscoveryFailed(t: String?, e: Int) {}
            override fun onDiscoveryStarted(t: String?) { status.value = "waiting: turn on 'Pair device with pairing code'" }
            override fun onDiscoveryStopped(t: String?) {}
            override fun onServiceLost(s: NsdServiceInfo?) {}
            override fun onServiceFound(s: NsdServiceInfo) {
                @Suppress("DEPRECATION")
                nsd.resolveService(s, object : NsdManager.ResolveListener {
                    override fun onResolveFailed(si: NsdServiceInfo?, e: Int) {}
                    override fun onServiceResolved(si: NsdServiceInfo) {
                        pairPort = si.port
                        status.value = "pairing port ${si.port} found — enter the code"
                        CmdLog.add("pairing service on port ${si.port}")
                        askForCode(ctx, si.port)
                    }
                })
            }
        }
        listener = l
        nsd.discoverServices("_adb-tls-pairing._tcp.", NsdManager.PROTOCOL_DNS_SD, l)
    }

    fun stopWatching(ctx: Context) {
        val l = listener ?: return
        listener = null
        runCatching { (ctx.getSystemService(Context.NSD_SERVICE) as NsdManager).stopServiceDiscovery(l) }
    }

    private fun askForCode(ctx: Context, port: Int) {
        val reply = PendingIntent.getBroadcast(ctx, 7,
            Intent(ctx, ActionReceiver::class.java).setAction(ActionReceiver.PAIR_CODE).putExtra("port", port),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE)
        val action = Notification.Action.Builder(null, "Enter code", reply)
            .addRemoteInput(RemoteInput.Builder(KEY_CODE).setLabel("6-digit pairing code").build()).build()
        val n = Notification.Builder(ctx, App.CH_PAIR)
            .setSmallIcon(R.drawable.ic_engine)
            .setContentTitle("MobileTokenSaver: pair now")
            .setContentText("Type the 6-digit code shown in Wireless debugging")
            .setAutoCancel(true)
            .addAction(action).build()
        (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).notify(NOTIF_ID, n)
    }

    /** Called from the notification reply. Pairs, then connects the shell channel. */
    fun submitCode(ctx: Context, port: Int, code: String) {
        val p = if (port > 0) port else pairPort
        if (p <= 0) { status.value = "no pairing port"; return }
        status.value = "pairing…"
        thread(name = "pair") {
            val ok = runCatching { AdbManager.get(ctx).pairWith("127.0.0.1", p, code.trim()) }
                .onFailure { CmdLog.add("pair failed: ${it.javaClass.simpleName}: ${it.message}") }
                .getOrDefault(false)
            if (ok) {
                CmdLog.add("paired")
                stopWatching(ctx)
                (ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).cancel(NOTIF_ID)
                val conn = runCatching { AdbManager.get(ctx).ensureConnected(ctx) }.getOrDefault(false)
                status.value = if (conn) "paired · shell connected" else "paired · connecting…"
            } else status.value = "pairing failed — check the code and try again"
        }
    }

    fun shellState(ctx: Context): String = runCatching {
        val m = AdbManager.get(ctx)
        // Read-only: the screen must not start connect attempts every few seconds (they hold the ADB lock and starve Keeper's repair).
        when {
            com.qcenaman.mobiletokensaver.engine.HelperClient.alive() -> "shell connected (helper via ${com.qcenaman.mobiletokensaver.engine.Privileged.via.ifEmpty { "earlier start" }})"
            m.isConnected -> "shell connected (adb)"
            m.needsPairing -> "shell not connected — pairing required"
            else -> "shell not connected"
        }
    }.getOrElse { "shell: ${it.message}" }
}
