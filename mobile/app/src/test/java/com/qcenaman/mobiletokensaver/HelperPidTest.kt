package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.helper.Main
import org.junit.Assert.assertEquals
import org.junit.Test

/** The helper's pid line: every pid length must parse (a 4-digit pid used to crash every command). */
class HelperPidTest {
    private fun pid(head: String) = Main.pidOf(head, head.indexOf('\n'))

    @Test fun anyPidLength() {
        assertEquals(7, pid("__MTS_PID=7\nout"))
        assertEquals(4321, pid("__MTS_PID=4321\nout"))
        assertEquals(12345, pid("__MTS_PID=12345\n"))
        assertEquals(4194303, pid("__MTS_PID=4194303\nmore"))
    }

    @Test fun garbageIsMinusOne() {
        assertEquals(-1, pid("__MTS_PID=\n"))
        assertEquals(-1, pid("__MTS_PID=x\n"))
        assertEquals(-1, Main.pidOf("__MTS", 3))
    }
}
