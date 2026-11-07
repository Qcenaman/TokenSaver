package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.adb.AdbManager
import io.github.muntashirakon.adb.android.AdbMdns
import java.net.InetAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Pairs our own ADB client with this phone's Wireless debugging from a code the user typed
 * (app screen, notification reply, or the PairReceiver broadcast). There is no automatic pairing:
 * the app never opens Settings or drives the screen by itself.
 */
object AutoPair {
    @Volatile var lastResult = ""

    private val codeRe = Regex("""^\d{6}$""")
    private val hostPortRe = Regex("""(\d{1,3}(?:\.\d{1,3}){3}):(\d{2,5})""")

    /** Pair with a known code; host/port discovered over mDNS when not given. */
    fun pairWithCode(ctx: Context, code: String, host: String? = null, port: Int = 0): Boolean {
        val (h, p) = if (host != null && port > 0) host to port else discoverPairingPort(ctx) ?: ("127.0.0.1" to port)
        if (p <= 0) { lastResult = "pairing port unknown"; CmdLog.add("pair: $lastResult"); return false }
        return try {
            val ok = AdbManager.get(ctx).pairWith(h, p, code.trim())
            lastResult = if (ok) "paired" else "pairing rejected"
            CmdLog.add("pair $h:$p -> $lastResult")
            if (ok) AdbManager.get(ctx).ensureConnected(ctx)
            ok
        } catch (e: Throwable) {
            lastResult = "pair failed: ${e.javaClass.simpleName}: ${e.message}"
            CmdLog.add(lastResult); false
        }
    }

    /** The pairing mDNS service is only advertised while the dialog is open. */
    private fun discoverPairingPort(ctx: Context, timeoutMs: Long = 5000): Pair<String, Int>? {
        val latch = CountDownLatch(1)
        var found: Pair<String, Int>? = null
        val mdns = AdbMdns(ctx, AdbMdns.SERVICE_TYPE_TLS_PAIRING) { addr: InetAddress?, port: Int ->
            if (port > 0) { found = (addr?.hostAddress ?: "127.0.0.1") to port; latch.countDown() }
        }
        mdns.start()
        latch.await(timeoutMs, TimeUnit.MILLISECONDS)
        mdns.stop()
        return found
    }
}
