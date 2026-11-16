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

    @Volatile private var worksAt = 0L
    @Volatile private var worksVal = false

    /** True only when the helper really runs a command (cached 10 s); a ping alone is not enough. */
    fun works(): Boolean {
        val now = System.currentTimeMillis()
        if (now - worksAt < 10_000) return worksVal
        val sb = StringBuilder()
        val c = if (alive()) exec("echo __mts_ok__") { b, n -> sb.append(String(b, 0, n)) } else -1
        worksVal = c == 0 && sb.contains("__mts_ok__"); worksAt = now
        return worksVal
    }

    private fun forget() { worksAt = 0L }

    private val lock = java.util.concurrent.locks.ReentrantLock()
    @Volatile private var attemptSince = 0L
    @Volatile private var attemptThread: Thread? = null

    /**
     * Runs [block] holding the start lock. When another start is under way the caller waits up to [waitMs] and gives up
     * instead of queueing for ever; an attempt that has been hanging longer than [HANG_MS] is broken off (its thread is
     * interrupted and the ADB connection dropped, which ends a stuck socket read), so the next call can start clean.
     */
    private fun <T> locked(ctx: Context, waitMs: Long, fallback: T, block: () -> T): T {
        if (!lock.tryLock(waitMs, java.util.concurrent.TimeUnit.MILLISECONDS)) {
            val since = attemptSince
            if (since > 0 && System.currentTimeMillis() - since > HANG_MS) {
                CmdLog.add("shell: start attempt hung ${(System.currentTimeMillis() - since) / 1000}s, breaking it off")
                attemptThread?.interrupt()
                runCatching { com.qcenaman.mobiletokensaver.adb.AdbManager.get(ctx).dropConnection() }
                attemptSince = 0L
            }
            return fallback
        }
        try { attemptSince = System.currentTimeMillis(); attemptThread = Thread.currentThread(); return block() }
        finally { attemptSince = 0L; attemptThread = null; lock.unlock() }
    }

    const val HANG_MS = 45_000L

    /** Start the shell-user helper through the first launcher that works: Shizuku, then our own ADB, then root. */
    fun ensureStarted(ctx: Context): Boolean {
        if (alive() && works()) return true
        return locked(ctx, 5_000, false) { if (alive() && works()) true else start(ctx, force = alive()) }
    }

    /** Replace a helper that answers but cannot run commands: kill it and its supervisor loop, start a fresh one. */
    fun relaunch(ctx: Context, reason: String): Boolean = locked(ctx, 5_000, false) {
        forget()
        if (works()) true else { CmdLog.add("shell helper replaced: $reason"); start(ctx, force = true) }
    }

    private fun start(ctx: Context, force: Boolean): Boolean {
        forget()
        val cmd = launchCmd(ctx.packageName, Prefs.helperSecret)
        for (l in Privileged.launchers) {
            if (!l.usable(ctx)) continue
            val launched = try { l.launch(ctx, cmd) } catch (e: Throwable) { CmdLog.add("${l.name}: ${e.javaClass.simpleName}: ${e.message}"); false }
            if (!launched) continue
            // launchCmd killed the old helper and loop first, so a ping now comes from the fresh one
            if ((1..14).any { Thread.sleep(500); alive() } && run { forget(); works() }) {
                Privileged.via = l.name
                CmdLog.add("shell helper started via ${l.name}${if (force) " (replaced a broken one)" else ""}")
                Privileged.afterStart(ctx)
                return true
            }
            CmdLog.add("${l.name}: helper did not come up (or could not run a command)")
        }
        return false
    }
}
