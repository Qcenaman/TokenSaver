package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.tools.ShellTools
import org.junit.Assert.assertEquals
import org.junit.Test

class ShellToolsTest {
    @Test fun defaultWaitStaysUnderThePcsSixtySecondPassThrough() {
        assertEquals(45, ShellTools.waitSeconds(emptyMap(), ShellTools.DEFAULT_WAIT_S))
        assert(ShellTools.DEFAULT_WAIT_S < 60)
    }

    @Test fun timeoutSecondsIsAnAliasAndWaitSecondsWins() {
        assertEquals(15, ShellTools.waitSeconds(mapOf("timeout_seconds" to 15), 45))
        assertEquals(15, ShellTools.waitSeconds(mapOf("timeout_seconds" to 15.0), 45))
        assertEquals(7, ShellTools.waitSeconds(mapOf("wait_seconds" to 7, "timeout_seconds" to 15), 45))
        assertEquals(1, ShellTools.waitSeconds(mapOf("wait_seconds" to 0), 45))
    }
}
