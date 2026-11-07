package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import com.qcenaman.mobiletokensaver.adb.AdbManager

/**
 * Shell access as the "shell" (uid 2000) user through the persistent helper (started via Shizuku, ADB or root);
 * falls back to the live ADB channel.
 */
object Shell {
    @Volatile var lastError: String = ""

    
    /** Capture combined output and exit code. */
    fun run(ctx: Context, cmd: String): Pair<Int, String> {
        val sb = StringBuilder()
        val code = exec(ctx, cmd) { b, n -> sb.append(String(b, 0, n, Charsets.UTF_8)) }
        return code to sb.toString()
    }

    /** Streamed execution; -1 when no channel is available. */
    fun exec(ctx: Context, cmd: String, onPid: (Int) -> Unit = {}, onData: (ByteArray, Int) -> Unit): Int {
        if (HelperClient.alive()) {
            val c = HelperClient.exec(cmd, onPid, onData)
            if (c != -1) return c
            // Answers pings but can't run commands: make it exit; its supervisor loop starts a fresh one.
            if (HelperClient.restart() && (1..16).any { Thread.sleep(500); HelperClient.alive() }) {
                val c2 = HelperClient.exec(cmd, onPid, onData)
                if (c2 != -1) return c2
            }
        }
        // Getting a channel (starting the helper, reconnecting ADB) takes locks and can stall on an odd network;
        // bound it so one stuck attempt cannot queue every tool behind it forever.
        val channel = withBudget(CHANNEL_BUDGET_MS) {
            if (HelperClient.ensureStarted(ctx)) "helper" else if ((Keeper.wifiUp(ctx) || AdbManager.loopbackOpen()) && AdbManager.get(ctx).ensureConnected(ctx)) "adb" else null
        }
        when (channel) {
            "helper" -> { val c = HelperClient.exec(cmd, onPid, onData); if (c != -1) return c }
            "adb" -> return AdbManager.get(ctx).exec(cmd, onPid, onData)
        }
        lastError = if (channel == null && budgetHit) "shell channel busy: a previous connect attempt is still hanging (open the app or reconnect Wi-Fi)"
            else "no shell channel: start Shizuku (and allow MobileTokenSaver in it), or pair Wireless debugging in the app"
        val m = lastError.toByteArray(); onData(m, m.size); return -1
    }

    const val CHANNEL_BUDGET_MS = 20_000L
    @Volatile private var budgetHit = false
    private val pool = java.util.concurrent.Executors.newCachedThreadPool { r -> Thread(r, "shell-channel").apply { isDaemon = true } }

    /** Runs [block] on a worker and gives up after [ms]; the worker keeps going so the lock it holds is released normally. */
    private fun <T> withBudget(ms: Long, block: () -> T?): T? {
        val f = pool.submit(java.util.concurrent.Callable { block() })
        return try { budgetHit = false; f.get(ms, java.util.concurrent.TimeUnit.MILLISECONDS) }
        catch (_: java.util.concurrent.TimeoutException) { budgetHit = true; null }
        catch (_: Exception) { null }
    }

    fun runBytes(ctx: Context, cmd: String): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        val code = exec(ctx, cmd) { b, n -> out.write(b, 0, n) }
        return if (code == -1 && out.size() < 200) ByteArray(0) else out.toByteArray()
    }

    fun status(ctx: Context): String = when {
        HelperClient.alive() -> "helper via ${Privileged.via.ifEmpty { "earlier start" }}"
        AdbManager.get(ctx).isConnected -> "adb (live)"
        else -> "down"
    }
}
