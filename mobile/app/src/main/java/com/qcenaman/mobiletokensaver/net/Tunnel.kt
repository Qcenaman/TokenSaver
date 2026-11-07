package com.qcenaman.mobiletokensaver.net

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.R
import com.qcenaman.mobiletokensaver.engine.HelperClient
import kotlinx.coroutines.flow.MutableStateFlow
import java.io.File
import kotlin.concurrent.thread

/**
 * Runs the bundled Tailscale Funnel node (libtsfunnel.so) and restarts it with backoff.
 * Its stdout lines "MTS LOGIN|URL|ERROR …" drive [state].
 */
object Tunnel {
    data class State(val status: String = "off", val url: String = "", val login: String = "", val error: String = "")

    /** Last lines the tunnel wrote to stderr (tsnet log), newest last. */
    val log = MutableStateFlow<List<String>>(emptyList())
    private fun addLog(l: String) { log.value = (log.value + l.take(300)).takeLast(60) }

    val state = MutableStateFlow(State())
    @Volatile private var proc: Process? = null
    @Volatile private var gen = 0
    private const val LOGIN_NOTIF = 44

    /** Parses one line from the tunnel into the new state (pure, for tests). */
    fun parse(s: State, line: String): State {
        val t = line.trim()
        if (!t.startsWith("MTS ")) return s
        val kind = t.substringAfter("MTS ").substringBefore(' ')
        val v = t.substringAfter("MTS $kind", "").trim()
        return when (kind) {
            "LOGIN" -> s.copy(status = "needs Tailscale sign-in", login = v, error = "")
            "URL" -> s.copy(status = "up", url = v, login = "", error = "")
            "ERROR" -> s.copy(status = "error", error = v)
            else -> s
        }
    }

    /** Backoff before the next restart: doubles from 2 s to 5 min, back to 2 s after a run of 2 min or more. */
    fun nextBackoff(prev: Long, ranMs: Long): Long = if (ranMs >= 120_000L) 2_000L else (prev * 2).coerceIn(2_000L, 300_000L)

    @Synchronized
    fun start(ctx: Context) {
        if (proc != null || remote || state.value.status.let { it != "off" && it != "stopped" }) return
        val my = ++gen
        val app = ctx.applicationContext
        thread(name = "tunnel", isDaemon = true) { loop(app, my) }
    }

    @Synchronized
    fun stop() {
        gen++
        proc?.destroy(); proc = null
        if (remote) killRemote()
        state.value = State("stopped")
    }

    /** Kill the running node; the loop starts a fresh one right away. */
    fun restart(ctx: Context) {
        val p = proc
        when {
            p != null -> p.destroy()
            remote -> killRemote()
            else -> { state.value = State("off"); start(ctx) }
        }
    }

    @Volatile private var remote = false
    private val proxy by lazy {
        val r = java.security.SecureRandom(); val pw = ByteArray(12).also { r.nextBytes(it) }.joinToString("") { "%02x".format(it) }
        ConnectProxy("mts", pw) { addLog(it) }
    }
    private const val REMOTE_DIR = "/data/local/tmp/mts"
    private const val REMOTE_BIN = "$REMOTE_DIR/mts-tsfunnel"

    private fun killRemote() = thread(isDaemon = true) { runCatching { HelperClient.exec("pkill -f $REMOTE_BIN") { _, _ -> } } }

    private fun loop(ctx: Context, my: Int) {
        var backoff = 1_000L
        while (gen == my) {
            val bin = File(ctx.applicationInfo.nativeLibraryDir, "libtsfunnel.so")
            if (!bin.exists()) { state.value = State("error", error = "tunnel binary missing from this build"); return }
            val started = System.currentTimeMillis()
            var lastErr = ""
            proxy.start()
            // Android drops internet traffic from the app's own processes on some phones; the shell user (our
            // Shizuku helper) is not restricted, so run the node there when the helper is up.
            if (Prefs.tunnelAsShell && (HelperClient.alive() || HelperClient.ensureStarted(ctx))) {
                remote = true
                state.value = state.value.copy(status = "starting", error = "")
                CmdLog.add("tunnel starting (as shell user)")
                val q = { x: String -> "'" + x.replace("'", "'\\''") + "'" }
                val cmd = "pkill -f $REMOTE_BIN 2>/dev/null; mkdir -p $REMOTE_DIR/tsnet && cp ${q(bin.path)} $REMOTE_BIN && chmod 755 $REMOTE_BIN && " +
                    "HTTPS_PROXY=${proxy.url()} HTTP_PROXY=${proxy.url()} NO_PROXY=127.0.0.1,localhost exec $REMOTE_BIN -v -dir $REMOTE_DIR/tsnet -hostname ${q(Prefs.tsHostname)} -target 127.0.0.1:${Prefs.port}"
                val buf = StringBuilder()
                val code = HelperClient.exec(cmd) { b, n ->
                    buf.append(String(b, 0, n, Charsets.UTF_8))
                    while (true) {
                        val i = buf.indexOf("\n"); if (i < 0) break
                        val line = buf.substring(0, i); buf.delete(0, i + 1)
                        if (line.startsWith("MTS ")) onLine(ctx, line) else if (line.isNotBlank()) { lastErr = line.take(300); addLog(line) }
                    }
                }
                if (gen == my) CmdLog.add("tunnel exited ($code)${if (lastErr.isNotEmpty()) ": $lastErr" else ""}")
                remote = false
            } else {
                val dir = File(ctx.filesDir, "tsnet").apply { mkdirs() }
                try {
                    val pb = ProcessBuilder(bin.path, "-v", "-dir", dir.path, "-hostname", Prefs.tsHostname, "-target", "127.0.0.1:${Prefs.port}")
                    pb.environment().apply {
                        put("HOME", dir.path); put("TMPDIR", ctx.cacheDir.path)
                        put("HTTPS_PROXY", proxy.url()); put("HTTP_PROXY", proxy.url()); put("NO_PROXY", "127.0.0.1,localhost")
                    }
                    val p = pb.start(); proc = p
                    state.value = state.value.copy(status = "starting", error = "")
                    CmdLog.add("tunnel starting (in app; shell helper not running)")
                    thread(isDaemon = true, name = "tunnel-err") { runCatching { p.errorStream.bufferedReader().forEachLine { if (it.isNotBlank()) { lastErr = it.take(300); addLog(it) } } } }
                    p.inputStream.bufferedReader().forEachLine { onLine(ctx, it) }
                    val code = p.waitFor()
                    if (gen == my) CmdLog.add("tunnel exited ($code)${if (lastErr.isNotEmpty()) ": $lastErr" else ""}")
                } catch (e: Exception) {
                    lastErr = "${e.javaClass.simpleName}: ${e.message}"
                    CmdLog.add("tunnel failed: $lastErr")
                }
                proc = null
            }
            if (gen != my) return
            val ran = System.currentTimeMillis() - started
            backoff = nextBackoff(backoff, ran)
            state.value = state.value.copy(status = "restarting in ${backoff / 1000}s", error = state.value.error.ifEmpty { lastErr })
            try { Thread.sleep(backoff) } catch (_: InterruptedException) {}
        }
    }

    private fun onLine(ctx: Context, line: String) {
        val before = state.value
        val now = parse(before, line)
        state.value = now
        if (now.login.isNotEmpty() && now.login != before.login) { CmdLog.add("tunnel: Tailscale sign-in needed"); loginNotice(ctx, now.login) }
        if (now.status == "up" && before.status != "up") { CmdLog.add("tunnel up: ${now.url}"); cancelLogin(ctx) }
        if (now.error.isNotEmpty() && now.error != before.error) CmdLog.add("tunnel: ${now.error}")
    }

    private fun loginNotice(ctx: Context, url: String) {
        val pi = PendingIntent.getActivity(ctx, 3, Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(ctx, App.CH_ALERT)
            .setSmallIcon(R.drawable.ic_engine)
            .setContentTitle("MobileTokenSaver: sign in to Tailscale")
            .setContentText("Tap to add the phone's Funnel node to your tailnet")
            .setContentIntent(pi).setAutoCancel(true).build()
        ctx.getSystemService(NotificationManager::class.java).notify(LOGIN_NOTIF, n)
    }

    private fun cancelLogin(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).cancel(LOGIN_NOTIF)
}
