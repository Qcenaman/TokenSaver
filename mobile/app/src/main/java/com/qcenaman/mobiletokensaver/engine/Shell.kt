package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.adb.AdbManager

/**
 * Shell access as the "shell" (uid 2000) user. Channels, in order:
 *   1. the persistent helper (started via Shizuku, our ADB or root) — judged by running a command, not by its ping;
 *      a helper that answers pings but can't run commands is replaced from scratch (old supervisor loop killed);
 *   2. Shizuku directly, one process per command (no helper needed, no locks);
 *   3. the live ADB channel.
 * Getting a channel is time-boxed and never queues behind a hung attempt. [state] says which channel works and why not.
 */
object Shell {
    @Volatile var lastError: String = ""
    /** The channel the last command went through ("helper via shizuku", "shizuku", "adb") and when. */
    @Volatile var lastChannel: String = ""
    @Volatile var lastOkAt: Long = 0
    @Volatile var lastFailAt: Long = 0

    /** Capture combined output and exit code. */
    fun run(ctx: Context, cmd: String): Pair<Int, String> {
        val sb = StringBuilder()
        val code = exec(ctx, cmd) { b, n -> sb.append(String(b, 0, n, Charsets.UTF_8)) }
        return code to sb.toString()
    }

    private fun ok(ch: String, code: Int): Int { lastChannel = ch; lastOkAt = System.currentTimeMillis(); lastError = ""; return code }

    /** Streamed execution; -1 when no channel is available. */
    fun exec(ctx: Context, cmd: String, onPid: (Int) -> Unit = {}, onData: (ByteArray, Int) -> Unit): Int {
        val why = ArrayList<String>()
        if (HelperClient.alive()) {
            val c = HelperClient.exec(cmd, onPid, onData)
            if (c != -1) return ok("helper via ${Privileged.via.ifEmpty { "earlier start" }}", c)
            why += "helper answers but can't run commands"
            // Broken helper: rebuild it from scratch (kills the old supervisor loop too), bounded in time.
            val rebuilt = withBudget(CHANNEL_BUDGET_MS) { if (HelperClient.relaunch(ctx, reason = "it answered but could not run commands")) true else null }
            if (rebuilt == true) {
                val c2 = HelperClient.exec(cmd, onPid, onData)
                if (c2 != -1) return ok("helper via ${Privileged.via}", c2)
                why += "fresh helper failed too"
            } else why += "helper rebuild ${if (budgetHit) "timed out" else "failed"}"
        }
        // Shizuku needs no helper and no lock: use it straight away when it is running and allowed.
        when (Privileged.shizukuState()) {
            Privileged.ShizukuState.READY -> {
                val c = runCatching { Privileged.ShizukuL.exec(cmd, onPid, onData) }.getOrElse { why += "shizuku: ${it.javaClass.simpleName}"; -1 }
                if (c != -1) { Keeper.kick(ctx); return ok("shizuku", c) }
            }
            Privileged.ShizukuState.NO_PERMISSION -> { why += "Shizuku running, not allowed yet (asking)"; Privileged.requestShizuku(); Keeper.kick(ctx) }
            Privileged.ShizukuState.NOT_RUNNING -> why += "Shizuku not running"
            Privileged.ShizukuState.OLD_VERSION -> why += "Shizuku too old"
        }
        // Starting the helper or connecting ADB takes locks and can stall on an odd network: time-boxed, and an
        // attempt that is already hanging is broken instead of waited on (see HelperClient / AdbManager).
        val channel = withBudget(CHANNEL_BUDGET_MS) {
            if (HelperClient.ensureStarted(ctx)) "helper"
            else if ((Keeper.wifiUp(ctx) || AdbManager.loopbackOpen()) && AdbManager.get(ctx).ensureConnected(ctx)) "adb" else null
        }
        when (channel) {
            "helper" -> { val c = HelperClient.exec(cmd, onPid, onData); if (c != -1) return ok("helper via ${Privileged.via}", c); why += "new helper failed" }
            "adb" -> { val c = AdbManager.get(ctx).exec(cmd, onPid, onData); if (c != -1) return ok("adb", c); why += "adb stream ended without exit code" }
            null -> why += if (budgetHit) "connect attempt timed out (it is broken off, next call retries)" else "helper/adb: ${AdbManager.get(ctx).lastConnectError.ifEmpty { "not connected" }}"
        }
        lastFailAt = System.currentTimeMillis()
        Keeper.kick(ctx)
        lastError = "no shell channel: start Shizuku (and allow MobileTokenSaver in it), or pair Wireless debugging in the app (${why.joinToString("; ")})"
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

    /** One line for `system` and the app screen: the channel that really works now (checked by running a command). */
    fun status(ctx: Context): String = when {
        HelperClient.works() -> "helper via ${Privileged.via.ifEmpty { "earlier start" }}"
        HelperClient.alive() -> "helper answers but can't run commands (being replaced)".also { Keeper.kick(ctx) }
        Privileged.shizukuState() == Privileged.ShizukuState.READY -> "shizuku (direct)"
        AdbManager.get(ctx).isConnected -> "adb (live)"
        else -> "down".also { Keeper.kick(ctx) }
    }

    /** Several lines: every channel's state and the last error (no secrets). */
    fun state(ctx: Context): String {
        val fmt = java.text.SimpleDateFormat("HH:mm:ss", java.util.Locale.US)
        fun t(ms: Long) = if (ms == 0L) "-" else fmt.format(java.util.Date(ms))
        val adb = AdbManager.get(ctx)
        return listOf(
            "channel: ${status(ctx)}",
            "last ok: ${t(lastOkAt)} via ${lastChannel.ifEmpty { "-" }}; last failure: ${t(lastFailAt)}",
            "helper: ${if (HelperClient.alive()) "running" else "not running"}, started via ${Privileged.via.ifEmpty { "-" }}",
            "shizuku: ${Privileged.shizukuState().name.lowercase().replace('_', ' ')}",
            "adb: ${if (adb.isConnected) "connected" else "not connected"}${if (adb.needsPairing) ", needs pairing" else ""}${adb.lastConnectError.let { if (it.isEmpty()) "" else ", last error: $it" }}",
        ).joinToString("\n") + (if (lastError.isNotEmpty() && lastFailAt > lastOkAt) "\nlast error: $lastError" else "")
    }

    @Suppress("unused") private fun log(s: String) = CmdLog.add(s)
}
