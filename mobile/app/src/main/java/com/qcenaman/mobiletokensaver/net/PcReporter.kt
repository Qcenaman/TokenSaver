package com.qcenaman.mobiletokensaver.net

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs

/**
 * "Link to my PC": tells the PC's TokenSaver how to reach this phone. Reports every [REPORT_EVERY_MS], when any
 * network (Wi-Fi, mobile data, the Tailscale VPN) comes or goes, when the phone's addresses change, and after
 * /link and pc status. Runs on its own thread, never behind a shell or ADB attempt. Does nothing while the
 * switch is off or no PC is paired.
 */
object PcReporter {
    const val REPORT_EVERY_MS = 20 * 60_000L
    @Volatile private var running = false
    @Volatile private var lastAddresses = ""

    private fun soon(ctx: Context, why: String, ms: Long) { if (Prefs.pcLinkOn) PcLink.reportSoon(ctx, why, ms) }

    fun start(ctx: Context) {
        if (running) return
        running = true
        val app = ctx.applicationContext
        val cm = app.getSystemService(ConnectivityManager::class.java)
        runCatching {
            cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(n: Network) = soon(app, "network up", 3000)
                override fun onLost(n: Network) = soon(app, "network lost", 5000)
                override fun onLinkPropertiesChanged(n: Network, lp: LinkProperties) { if (addressesChanged()) soon(app, "ip change", 3000) }
            })
        }
        runCatching {
            cm.registerNetworkCallback(
                NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_VPN).removeCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build(),
                object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(n: Network) = soon(app, "vpn up", 4000)
                    override fun onLost(n: Network) = soon(app, "vpn lost", 3000)
                })
        }
        Thread {
            try { Thread.sleep(8_000) } catch (_: InterruptedException) {}
            while (running) {
                try {
                    if (Prefs.pcLinkOn && PcLink.pc() != null) {
                        val since = System.currentTimeMillis() - maxOf(PcLink.lastReportAt, PcLink.lastReportTry)
                        if (addressesChanged()) PcLink.report(app, why = "ip change")
                        else if (since >= REPORT_EVERY_MS) PcLink.report(app, why = "timer")
                        else if (PcLink.lastReportError.isNotEmpty() && since >= 2 * 60_000L) PcLink.report(app, why = "retry")
                    }
                } catch (_: Exception) {}
                try { Thread.sleep(60_000) } catch (_: InterruptedException) {}
            }
        }.apply { isDaemon = true; name = "pc-reporter" }.start()
    }

    /** True (and remembers) when the phone's LAN/Tailscale addresses differ from the last look. */
    private fun addressesChanged(): Boolean {
        val now = Addresses.summary()
        if (now == lastAddresses) return false
        val first = lastAddresses.isEmpty()
        lastAddresses = now
        if (!first) CmdLog.add("addresses changed: $now")
        return !first
    }

    fun stop() { running = false }
}
