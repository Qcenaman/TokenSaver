package com.qcenaman.mobiletokensaver.net

import java.net.Inet4Address
import java.net.NetworkInterface

/** The phone's own addresses: LAN (wlan) and Tailscale (100.64.0.0/10). */
object Addresses {
    data class Found(val lan: String?, val tailscale: String?)

    fun find(): Found {
        var lan: String? = null; var ts: String? = null
        try {
            for (ni in NetworkInterface.getNetworkInterfaces()) {
                for (a in ni.inetAddresses) {
                    if (a !is Inet4Address || a.isLoopbackAddress) continue
                    val ip = a.hostAddress ?: continue
                    val first = ip.substringBefore('.').toIntOrNull() ?: continue
                    val second = ip.split('.').getOrNull(1)?.toIntOrNull() ?: continue
                    if (first == 100 && second in 64..127) ts = ip
                    else if (ni.name.startsWith("wlan")) lan = ip
                }
            }
        } catch (_: Exception) {}
        return Found(lan, ts)
    }

    fun summary(): String = find().let { "lan=${it.lan ?: "-"} tailscale=${it.tailscale ?: "-"}" }
}
