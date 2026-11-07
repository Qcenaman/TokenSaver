package com.qcenaman.mobiletokensaver.adb

import android.content.Context
import android.os.Build
import com.qcenaman.mobiletokensaver.CmdLog
import io.github.muntashirakon.adb.AbsAdbConnectionManager
import io.github.muntashirakon.adb.AdbStream
import org.bouncycastle.asn1.x500.X500Name
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder
import java.io.ByteArrayOutputStream
import java.io.File
import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.PrivateKey
import java.security.cert.Certificate
import java.security.cert.CertificateFactory
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Date
import java.util.concurrent.TimeUnit

/** Our own ADB client: pairs with this phone's Wireless debugging and talks to adbd on localhost. */
class AdbManager private constructor(dir: File) : AbsAdbConnectionManager() {
    @Volatile var needsPairing = false
        private set
    private val key: PrivateKey
    private val cert: Certificate

    init {
        setApi(Build.VERSION.SDK_INT)
        setTimeout(10, TimeUnit.SECONDS)
        dir.mkdirs()
        val kf = File(dir, "adbkey.pk8")
        val cf = File(dir, "adbkey.crt")
        if (kf.exists() && cf.exists()) {
            key = KeyFactory.getInstance("RSA").generatePrivate(PKCS8EncodedKeySpec(kf.readBytes()))
            cert = CertificateFactory.getInstance("X.509").generateCertificate(cf.inputStream())
        } else {
            val kpg = KeyPairGenerator.getInstance("RSA").apply { initialize(2048) }
            val kp = kpg.generateKeyPair()
            val name = X500Name("CN=MobileTokenSaver")
            val now = System.currentTimeMillis()
            val holder = JcaX509v3CertificateBuilder(
                name, BigInteger.valueOf(now), Date(now - 86_400_000L), Date(now + 30L * 365 * 86_400_000L), name, kp.public
            ).build(JcaContentSignerBuilder("SHA256withRSA").build(kp.private))
            key = kp.private
            cert = JcaX509CertificateConverter().getCertificate(holder)
            kf.writeBytes(key.encoded)
            cf.writeBytes(cert.encoded)
        }
    }

    override fun getPrivateKey(): PrivateKey = key
    override fun getCertificate(): Certificate = cert
    override fun getDeviceName(): String = "MobileTokenSaver"

    /** Connect to this phone's adbd (discovers the TLS port over mDNS). */
    @Synchronized
    fun ensureConnected(ctx: Context): Boolean {
        if (isConnected) return true
        if (connectLoopback()) { needsPairing = false; CmdLog.add("adb connected over loopback (no Wi-Fi needed)"); return true }
        return try {
            val ok = connectTls(ctx, 8_000) || autoConnect(ctx, 5_000)
            if (ok) { needsPairing = false; CmdLog.add("adb connected"); switchToTcp() }
            ok
        } catch (e: io.github.muntashirakon.adb.AdbPairingRequiredException) {
            needsPairing = true
            CmdLog.add("adb: pairing required")
            false
        } catch (e: Throwable) {
            CmdLog.add("adb connect failed: ${e.javaClass.simpleName}: ${e.message}")
            false
        }
    }

    private fun connectLoopback(): Boolean =
        loopbackOpen() && runCatching { connect("127.0.0.1", TCP_PORT) }.getOrDefault(false)

    /**
     * Put adbd in TCP mode (like `adb tcpip 5555`): it then also answers on 127.0.0.1, so the app reaches its own
     * shell without Wi-Fi until the next reboot. adbd restarts, so reconnect over loopback afterwards.
     */
    private fun switchToTcp() {
        if (loopbackOpen()) return
        runCatching { openStream("tcpip:$TCP_PORT").use { it.openInputStream().readBytes() } }
        runCatching { disconnect() }
        for (i in 1..20) {
            Thread.sleep(500)
            if (connectLoopback()) { CmdLog.add("adb now on loopback :$TCP_PORT — shell works without Wi-Fi until reboot"); return }
        }
        CmdLog.add("adb: TCP mode did not come up on loopback")
    }

    fun pairWith(host: String, port: Int, code: String): Boolean = pair(host, port, code)

    /**
     * Runs [cmd] in sh as the shell user; streams merged stdout/stderr to [onData].
     * Returns the exit code (or -1 when the stream ended without one).
     */
    fun exec(cmd: String, onPid: (Int) -> Unit = {}, onData: (ByteArray, Int) -> Unit): Int {
        val b64 = android.util.Base64.encodeToString(cmd.toByteArray(), android.util.Base64.NO_WRAP)
        val wrapped = "sh -c \"\$(echo $b64 | base64 -d)\" </dev/null 2>&1 & p=\$!; echo $MARK_PID\$p; wait \$p; r=\$?; echo; echo $MARK_EXIT\$r"
        val s: AdbStream = openStream("shell:$wrapped")
        var exit = -1
        // The last output line is held back until the next one arrives: the "echo" before the exit marker adds an empty
        // line when the command's output did not end with a newline, and that extra line must not reach the caller.
        var pending: ByteArray? = null
        s.openInputStream().use { inp ->
            val lineBuf = ByteArrayOutputStream()
            val buf = ByteArray(16384)
            while (true) {
                // adbd closing the stream surfaces as an IOException here: that is a normal end of output.
                val n = try { inp.read(buf) } catch (e: java.io.IOException) { -1 }
                if (n < 0) break
                // Pass bytes through, but strip our marker lines.
                var start = 0
                for (i in 0 until n) {
                    if (buf[i] == '\n'.code.toByte()) {
                        lineBuf.write(buf, start, i - start + 1)
                        val line = lineBuf.toString(Charsets.UTF_8.name())
                        when {
                            line.startsWith(MARK_PID) -> line.removePrefix(MARK_PID).trim().toIntOrNull()?.let(onPid)
                            line.startsWith(MARK_EXIT) -> {
                                exit = line.removePrefix(MARK_EXIT).trim().toIntOrNull() ?: -1
                                pending?.let { if (!(it.size == 1 && it[0] == '\n'.code.toByte())) onData(it, it.size) }; pending = null
                            }
                            else -> { pending?.let { onData(it, it.size) }; pending = lineBuf.toByteArray() }
                        }
                        lineBuf.reset()
                        start = i + 1
                    }
                }
                if (start < n) lineBuf.write(buf, start, n - start)
            }
            pending?.let { onData(it, it.size) }
            if (lineBuf.size() > 0) { val bb = lineBuf.toByteArray(); onData(bb, bb.size) }
        }
        try { s.close() } catch (_: Exception) {}
        return exit
    }

    fun capture(cmd: String): Pair<Int, String> {
        val out = ByteArrayOutputStream()
        val code = exec(cmd) { b, n -> out.write(b, 0, n) }
        return code to out.toString(Charsets.UTF_8.name())
    }

    /** Raw binary output of a command (exec: service, no pty, no markers). */
    fun execBytes(cmd: String): ByteArray {
        val s = openStream("exec:$cmd")
        val out = ByteArrayOutputStream()
        s.openInputStream().use { it.copyTo(out) }
        try { s.close() } catch (_: Exception) {}
        return out.toByteArray()
    }

    companion object {
        private const val MARK_PID = "__MTS_PID="
        private const val MARK_EXIT = "__MTS_EXIT="
        @Volatile private var inst: AdbManager? = null
        const val TCP_PORT = 5555

        /** adbd in TCP mode answers here (set once per boot by [switchToTcp]). */
        fun loopbackOpen(): Boolean = runCatching {
            java.net.Socket().use { it.connect(java.net.InetSocketAddress("127.0.0.1", TCP_PORT), 300) }; true
        }.getOrDefault(false)

        fun get(ctx: Context): AdbManager =
            inst ?: synchronized(this) { inst ?: AdbManager(File(ctx.filesDir, "adb")).also { inst = it } }
    }
}
