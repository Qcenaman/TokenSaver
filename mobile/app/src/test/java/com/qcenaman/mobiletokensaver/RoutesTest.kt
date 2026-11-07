package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.net.Routes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RoutesTest {
    @Test fun orderIsLanTailscalePublicTunnel() {
        val r = Routes.of("192.168.1.5", "100.100.1.2", "myhome.duckdns.org", "https://abc.trycloudflare.com/", 8766)
        assertEquals(listOf("lan", "tailscale", "public", "tunnel"), r.map { it.via })
        assertEquals("http://192.168.1.5:8766", r[0].base)
        assertEquals("http://100.100.1.2:8766", r[1].base)
        assertEquals("http://myhome.duckdns.org:8766", r[2].base)
        assertEquals("https://abc.trycloudflare.com", r[3].base)
    }

    @Test fun skipsEmptyRoutesSoTailscaleIsOptional() {
        val r = Routes.of("", "", "", "tunnel.example.com", 8766)
        assertEquals(listOf("tunnel"), r.map { it.via })
        assertEquals("https://tunnel.example.com", r[0].base)
    }

    @Test fun publicHostForms() {
        assertEquals("http://1.2.3.4:9000", Routes.publicBase("1.2.3.4:9000", 8766))
        assertEquals("http://1.2.3.4:8766", Routes.publicBase("1.2.3.4", 8766))
        assertEquals("https://pc.example.com", Routes.publicBase("https://pc.example.com/", 8766))
        assertNull(Routes.publicBase("  ", 8766))
        assertEquals("myhome.duckdns.org", Routes.publicHostOnly("myhome.duckdns.org:8766"))
        assertEquals("1.2.3.4", Routes.publicHostOnly("http://1.2.3.4:8766/"))
        assertNull(Routes.publicHostOnly(""))
    }

    @Test fun probeReasonsAreShort() {
        assertEquals("timeout 4000ms", Routes.reason(java.net.SocketTimeoutException("connect timed out"), 4000))
        assertEquals("connect refused", Routes.reason(java.net.ConnectException("Connection refused"), 1500))
        assertEquals("dns", Routes.reason(java.net.UnknownHostException("pc.example.com"), 5000))
        assertEquals("no route to host", Routes.reason(java.net.SocketException("ENETUNREACH (Network is unreachable)"), 1500))
    }
}
