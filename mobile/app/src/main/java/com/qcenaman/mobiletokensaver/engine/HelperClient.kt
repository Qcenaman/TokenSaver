package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import java.net.InetSocketAddress
import java.net.Socket
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.helper.Main
import java.io.DataInputStream
import java.io.DataOutputStream

/** Talks to the shell-user helper process; starts it over ADB when it is not running. */
object HelperClient {
    /** How long to wait for the helper's first frame (its pid) before treating it as hung. */
    const val FIRST_FRAME_MS = 15_000
    private fun connect(): Socket? = try {
        Socket().also { it.connect(InetSocketAddress("127.0.0.1", Main.PORT), 1500); it.soTimeout = 0 }
    } catch (_: Exception) { null }

    fun alive(): Boolean {
        val s = connect() ?: return false
        return try {
            val out = DataOutputStream(s.getOutputStream()); val inp = DataInputStream(s.getInputStream())
            out.write((Prefs.helperSecret + "\n").toByteArray()); val c = "__ping__".toByteArray(); out.writeInt(c.size); out.write(c); out.flush()
            s.soTimeout = 3000
            inp.readByte().toInt() == 3
        } catch (_: Exception) { false } finally { try { s.close() } catch (_: Exception) {} }
    }

    /** Streams merged output; returns exit code (-1 if the helper vanished). */
    fun exec(cmd: String, onPid: (Int) -> Unit = {}, onData: (ByteArray, Int) -> Unit): Int {
        val s = connect() ?: return -1
        try {
            val out = DataOutputStream(s.getOutputStream()); val inp = DataInputStream(s.getInputStream())
            out.write((Prefs.helperSecret + "\n").toByteArray()); val c = cmd.toByteArray(); out.writeInt(c.size); out.write(c); out.flush()
            // The helper answers with the pid frame at once; a helper that accepted but never answers is dead, not slow.
            s.soTimeout = FIRST_FRAME_MS
            while (true) {
                val type = inp.read(); if (type < 0) { CmdLog.add("helper exec failed: connection closed"); return -1 }
                val n = inp.readInt(); val payload = ByteArray(n).also { inp.readFully(it) }
                s.soTimeout = 0
                when (type) {
                    1 -> onData(payload, n)
                    2 -> onPid(int(payload))
                    3 -> return int(payload)
                }
            }
        } catch (e: Exception) { CmdLog.add("helper exec failed: ${e.javaClass.simpleName}: ${e.message}"); return -1 } finally { try { s.close() } catch (_: Exception) {} }
    }

    /** Ask the running helper to exit; its supervisor loop starts a fresh one from the current APK (used after an update). */
    fun restart(): Boolean {
        val s = connect() ?: return false
        return try {
            val out = DataOutputStream(s.getOutputStream())
            out.write((Prefs.helperSecret + "\n").toByteArray()); val c = "__exit__".toByteArray(); out.writeInt(c.size); out.write(c); out.flush()
            CmdLog.add("shell helper asked to restart from the new APK"); true
        } catch (_: Exception) { false } finally { try { s.close() } catch (_: Exception) {} }
    }

    private fun int(b: ByteArray) = ((b[0].toInt() and 255) shl 24) or ((b[1].toInt() and 255) shl 16) or ((b[2].toInt() and 255) shl 8) or (b[3].toInt() and 255)

    /** Shell line that (re)starts the helper under a supervisor loop; run it with any privileged launcher. */
    fun launchCmd(pkg: String, secret: String): String =
        // A helper from an older build (or with another secret) would block the port: replace it. The supervisor loop
        // (detached with setsid) restarts the helper within 5 s if it dies and looks the APK path up each time, so after
        // an app update the helper comes back from the new APK.
        "for p in \$(pgrep -f com.qcenaman.mobiletokensaver.helper.Main); do [ \$p != \$\$ ] && kill -9 \$p; done 2>/dev/null; sleep 0.3; " +
            "nohup setsid sh -c 'while :; do CLASSPATH=\$(pm path $pkg | head -1 | cut -d: -f2) app_process /system/bin com.qcenaman.mobiletokensaver.helper.Main $secret; sleep 5; done' >/dev/null 2>&1 &"

    /** Start the shell-user helper through the first launcher that works: Shizuku, then our own ADB, then root. */
    @Synchronized
    fun ensureStarted(ctx: Context): Boolean {
        if (alive()) return true
        val cmd = launchCmd(ctx.packageName, Prefs.helperSecret)
        for (l in Privileged.launchers) {
            if (!l.usable(ctx)) continue
            val launched = try { l.launch(ctx, cmd) } catch (e: Throwable) { CmdLog.add("${l.name}: ${e.javaClass.simpleName}: ${e.message}"); false }
            if (!launched) continue
            if ((1..10).any { Thread.sleep(500); alive() }) {
                Privileged.via = l.name
                CmdLog.add("shell helper started via ${l.name}")
                Privileged.afterStart(ctx)
                return true
            }
            CmdLog.add("${l.name}: helper did not come up")
        }
        return false
    }
}
