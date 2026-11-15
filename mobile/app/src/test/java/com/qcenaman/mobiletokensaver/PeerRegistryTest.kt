package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.mcp.Json
import com.qcenaman.mobiletokensaver.mcp.asMap
import com.qcenaman.mobiletokensaver.net.PeerRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PeerRegistryTest {
    private fun entry(json: String) = Json.parse(json).asMap()

    @Test fun readsGoTimes() {
        assertEquals(1_700_000_000_123L, PeerRegistry.parseTime("2023-11-14T22:13:20.123456789Z"))
        assertEquals(PeerRegistry.parseTime("2026-11-11T18:00:00+08:00"), PeerRegistry.parseTime("2026-11-11T10:00:00Z"))
        assertEquals(0L, PeerRegistry.parseTime(null))
        assertEquals(0L, PeerRegistry.parseTime("garbage"))
        assertEquals(1_700_000_000_000L, PeerRegistry.parseTime(PeerRegistry.goTime(1_700_000_000_000L)))
    }

    @Test fun newestAnnouncementWinsAndKeepsKnownFields() {
        val m = linkedMapOf<String, Map<String, Any?>>(
            "ultimateviruscollection" to entry("""{"ip":"100.109.168.97","lan":"192.168.1.10","mac":"aa:bb:cc:dd:ee:ff","port":8766,"token":"old","kind":"computer","updated":"2026-11-10T00:00:00Z"}"""),
            "mcam-beelink" to entry("""{"ip":"100.104.221.41","port":8766,"token":"m1","paired":"2026-11-11T00:00:00Z"}"""),
        )
        val incoming = entry("""{
            "UltimateVirusCollection":{"ip":"100.109.168.97","token":"new","updated":"2026-11-15T00:00:00Z"},
            "mcam-beelink":{"token":"stale","updated":"2026-11-01T00:00:00Z"},
            "iris-s24-ultra":{"token":"me","updated":"2026-11-15T00:00:00Z"},
            "tv":{"ip":"100.70.1.2","kind":"android","token":"tv"},
            "notoken":{"ip":"1.2.3.4"}}""")
        val ch = PeerRegistry.merge(m, incoming, "iris-s24-ultra")
        assertEquals(listOf("tv", "ultimateviruscollection"), ch)
        val d = m["ultimateviruscollection"]!!
        assertEquals("new", d["token"]); assertEquals("192.168.1.10", d["lan"]); assertEquals("aa:bb:cc:dd:ee:ff", d["mac"])
        assertEquals("m1", m["mcam-beelink"]!!["token"])
        assertFalse(m.containsKey("iris-s24-ultra")); assertFalse(m.containsKey("notoken"))
        assertEquals(PeerRegistry.ANDROID_PORT, PeerRegistry.port(m["tv"]!!))
        assertTrue(PeerRegistry.merge(m, incoming, "iris-s24-ultra").isEmpty())
    }

    @Test fun relayOrderPrimaryThenAndroidThenComputers() {
        val m = linkedMapOf<String, Map<String, Any?>>(
            "mcam-beelink" to entry("""{"token":"a"}"""), "tablet" to entry("""{"token":"b","kind":"android"}"""),
            "ultimateviruscollection" to entry("""{"token":"c"}"""), "omegajin-g1619-04" to entry("""{"token":"d"}"""),
            "notoken" to entry("""{}"""))
        assertEquals(listOf("ultimateviruscollection", "tablet", "mcam-beelink"),
            PeerRegistry.relays("omegajin-g1619-04", "iris-s24-ultra", m, "ultimateviruscollection"))
    }

    @Test fun routesLanTailscaleTunnel() {
        val r = PeerRegistry.routes(entry("""{"lan":"192.168.1.119","ip":"100.104.221.41","tunnel":"pc.example.ts.net","token":"x"}"""))
        assertEquals(listOf("lan", "tailscale", "tunnel"), r.map { it.via })
        assertEquals("http://192.168.1.119:8766", r[0].base)
        assertEquals("https://pc.example.ts.net", r[2].base)
    }

    @Test fun importsOldLinkFiles() {
        val (n, e) = PeerRegistry.fromLinkFile("/data/local/tmp/michie-tokensaver.json",
            entry("""{"name":"MCAM-Beelink","lan":"192.168.1.119","tailscale":"100.104.221.41","port":8766,"token":"t"}"""))!!
        assertEquals("mcam-beelink", n); assertEquals("100.104.221.41", e["ip"]); assertEquals("192.168.1.119", e["lan"])
        val (n2, _) = PeerRegistry.fromLinkFile("/data/local/tmp/laptop-tokensaver.json", entry("""{"ip":"100.109.184.29","token":"t2"}"""))!!
        assertEquals("laptop", n2)
        assertNull(PeerRegistry.fromLinkFile("x.json", entry("""{"ip":"1.2.3.4"}""")))
    }

    @Test fun relayUnableTellsCantReachFromToolErrors() {
        assertTrue(PeerRegistry.relayUnable("peer-unreachable: ultimateviruscollection: lan timeout"))
        assertTrue(PeerRegistry.relayUnable("unknown tool: peer"))
        assertFalse(PeerRegistry.relayUnable("exit=1 file not found"))
    }
}
