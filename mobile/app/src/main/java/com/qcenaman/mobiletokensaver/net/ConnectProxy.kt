package com.qcenaman.mobiletokensaver.net

import java.io.InputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.Base64
import kotlin.concurrent.thread

/**
 * Loopback HTTP proxy (CONNECT, plus absolute-URI GET/POST) for the tunnel. Android only routes sockets that the
 * app's runtime tags for the right network; the static Go tunnel's own sockets go nowhere, so it reaches the
 * internet through these app sockets instead. Requires the Proxy-Authorization the tunnel is given.
 */
class ConnectProxy(private val user: String, private val pass: String, private val log: (String) -> Unit) {
    private var server: ServerSocket? = null
    val port: Int get() = server?.localPort ?: 0
    fun url() = "http://$user:$pass@127.0.0.1:$port"

    @Synchronized fun start(): Int {
        server?.let { return it.localPort }
        val s = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"))
        server = s
        thread(isDaemon = true, name = "mts-proxy") {
            while (!s.isClosed) {
                val c = try { s.accept() } catch (_: Exception) { break }
                thread(isDaemon = true) { handle(c) }
            }
        }
        return s.localPort
    }

    private fun handle(c: Socket) {
        try {
            c.soTimeout = 30_000
            val inp = c.getInputStream()
            val head = readHead(inp) ?: return c.close()
            val lines = head.split("\r\n")
            val parts = lines[0].split(" ")
            if (parts.size < 3) return c.close()
            val auth = lines.drop(1).firstOrNull { it.startsWith("proxy-authorization:", true) }?.substringAfter(':')?.trim().orEmpty()
            val want = "Basic " + Base64.getEncoder().encodeToString("$user:$pass".toByteArray())
            if (auth != want) { c.getOutputStream().write("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\nContent-Length: 0\r\n\r\n".toByteArray()); return c.close() }
            val method = parts[0]
            val up: Socket
            if (method.equals("CONNECT", true)) {
                val (host, port) = hostPort(parts[1], 443)
                up = dial(host, port) ?: return fail(c, parts[1])
                c.getOutputStream().write("HTTP/1.1 200 Connection established\r\n\r\n".toByteArray())
            } else {
                val uri = java.net.URI(parts[1])
                val port = if (uri.port > 0) uri.port else 80
                up = dial(uri.host, port) ?: return fail(c, parts[1])
                val path = (uri.rawPath ?: "/").ifEmpty { "/" } + (uri.rawQuery?.let { "?$it" } ?: "")
                val rest = lines.drop(1).filterNot { it.startsWith("proxy-", true) }.joinToString("\r\n")
                up.getOutputStream().write("$method $path ${parts[2]}\r\n$rest\r\n\r\n".toByteArray())
            }
            c.soTimeout = 0
            pipe(c.getInputStream(), up.getOutputStream(), up)
            pipe(up.getInputStream(), c.getOutputStream(), c)
        } catch (e: Exception) {
            runCatching { c.close() }
        }
    }

    private fun dial(host: String, port: Int): Socket? = try {
        Socket().apply { connect(InetSocketAddress(host, port), 15_000); tcpNoDelay = true }
    } catch (e: Exception) { log("proxy: $host:$port failed: ${e.javaClass.simpleName}: ${e.message}"); null }

    private fun fail(c: Socket, what: String) {
        runCatching { c.getOutputStream().write("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n".toByteArray()); c.close() }
    }

    private fun pipe(i: InputStream, o: OutputStream, closeAlso: Socket) = thread(isDaemon = true) {
        val b = ByteArray(32 * 1024)
        try { while (true) { val n = i.read(b); if (n < 0) break; o.write(b, 0, n); o.flush() } } catch (_: Exception) {}
        runCatching { closeAlso.shutdownOutput() }
    }

    companion object {
        /** Reads up to the blank line ending the request head (max 16 KB). */
        fun readHead(i: InputStream): String? {
            val sb = StringBuilder()
            while (sb.length < 16 * 1024) {
                val b = i.read(); if (b < 0) return null
                sb.append(b.toChar())
                if (sb.endsWith("\r\n\r\n")) return sb.substring(0, sb.length - 4)
            }
            return null
        }

        fun hostPort(s: String, def: Int): Pair<String, Int> {
            if (s.startsWith("[")) { val h = s.substring(1, s.indexOf(']')); return h to (s.substringAfter("]:", "").toIntOrNull() ?: def) }
            val i = s.lastIndexOf(':')
            return if (i > 0) s.substring(0, i) to (s.substring(i + 1).toIntOrNull() ?: def) else s to def
        }
    }
}
