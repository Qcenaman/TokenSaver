package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.net.Tunnel
import org.junit.Assert.assertEquals
import org.junit.Test

class TunnelTest {
    @Test fun parsesEvents() {
        var s = Tunnel.State("starting")
        s = Tunnel.parse(s, "MTS LOGIN https://login.tailscale.com/a/x1")
        assertEquals("https://login.tailscale.com/a/x1", s.login)
        s = Tunnel.parse(s, "noise from tsnet")
        s = Tunnel.parse(s, "MTS URL https://mobiletokensaver.tail90c428.ts.net")
        assertEquals("up", s.status); assertEquals("", s.login)
        assertEquals("https://mobiletokensaver.tail90c428.ts.net", s.url)
        s = Tunnel.parse(s, "MTS ERROR funnel: not allowed")
        assertEquals("funnel: not allowed", s.error)
    }

    @Test fun backoff() {
        assertEquals(2_000L, Tunnel.nextBackoff(1_000L, 500))
        assertEquals(8_000L, Tunnel.nextBackoff(4_000L, 500))
        assertEquals(300_000L, Tunnel.nextBackoff(300_000L, 500))
        assertEquals(2_000L, Tunnel.nextBackoff(300_000L, 200_000))
    }
}
