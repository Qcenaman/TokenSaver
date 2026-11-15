package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.engine.PairText
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PairTextTest {
    @Test fun readsAospDialog() {
        val f = PairText.parse(listOf("Pair with device", "Wi-Fi pairing code", "482913", "IP address & Port", "192.168.1.42:37105", "Cancel"))
        assertEquals("482913", f.code); assertEquals("192.168.1.42", f.host); assertEquals(37105, f.port)
    }

    @Test fun readsSpacedCodeFromOcr() {
        val f = PairText.parse(listOf("Wi-Fi pairing code", "482 913", "192.168.1.42 : 37105"))
        assertEquals("482913", f.code); assertEquals(37105, f.port)
    }

    @Test fun portIsNotTakenForCode() {
        val f = PairText.parse(listOf("IP address & Port 10.0.0.5:412345"))
        assertNull(f.code)
    }

    @Test fun nothingOnScreen() {
        val f = PairText.parse(listOf("Wireless debugging", "Pair device with pairing code"))
        assertNull(f.code); assertEquals(0, f.port)
    }
}
