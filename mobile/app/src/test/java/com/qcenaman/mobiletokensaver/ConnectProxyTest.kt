package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.net.ConnectProxy
import org.junit.Assert.assertEquals
import org.junit.Test
import java.net.ServerSocket
import java.net.Socket
import java.util.Base64
import kotlin.concurrent.thread

class ConnectProxyTest {
    @Test fun connectTunnelsBytesAndNeedsAuth() {
        val echo = ServerSocket(0)
        thread(isDaemon = true) { val c = echo.accept(); val b = ByteArray(5); c.getInputStream().read(b); c.getOutputStream().write(b); c.getOutputStream().flush() }
        val p = ConnectProxy("u", "p") {}
        val port = p.start()

        Socket("127.0.0.1", port).use { s ->
            s.getOutputStream().write("CONNECT 127.0.0.1:${echo.localPort} HTTP/1.1\r\n\r\n".toByteArray())
            assertEquals(true, ConnectProxy.readHead(s.getInputStream())!!.startsWith("HTTP/1.1 407"))
        }
        Socket("127.0.0.1", port).use { s ->
            val auth = Base64.getEncoder().encodeToString("u:p".toByteArray())
            s.getOutputStream().write("CONNECT 127.0.0.1:${echo.localPort} HTTP/1.1\r\nProxy-Authorization: Basic $auth\r\n\r\n".toByteArray())
            assertEquals(true, ConnectProxy.readHead(s.getInputStream())!!.startsWith("HTTP/1.1 200"))
            s.getOutputStream().write("hello".toByteArray())
            val b = ByteArray(5); var n = 0
            while (n < 5) { val r = s.getInputStream().read(b, n, 5 - n); if (r < 0) break; n += r }
            assertEquals("hello", String(b))
        }
    }

    @Test fun hostPort() {
        assertEquals("a.b" to 443, ConnectProxy.hostPort("a.b:443", 80))
        assertEquals("::1" to 8080, ConnectProxy.hostPort("[::1]:8080", 80))
        assertEquals("x" to 80, ConnectProxy.hostPort("x", 80))
    }
}
