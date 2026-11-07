package com.qcenaman.mobiletokensaver.net

import android.content.Context
import android.os.BatteryManager
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.mcp.Json
import com.qcenaman.mobiletokensaver.mcp.ToolResult
import com.qcenaman.mobiletokensaver.mcp.asMap
import com.qcenaman.mobiletokensaver.mcp.int
import com.qcenaman.mobiletokensaver.mcp.string
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.URL

/**
 * The PC's TokenSaver "link" front (tokensaver serve), reached over the first route that answers:
 * LAN → Tailscale → public address (DDNS/port-forward) → HTTPS tunnel. The last two need no Tailscale.
 * Details arrive from the PC's `phone pair` (POST /link), are refreshed by the PC's reply to
 * /phone/report (its tunnel URL can change), can be set by hand (pc route), and live in Prefs.
 */
object PcLink {
    data class Pc(val token: String, val port: Int, val lanIp: String, val tsIp: String, val mac: String, val name: String,
                  val publicHost: String, val tunnelUrl: String) {
        fun routes() = Routes.of(lanIp, tsIp, publicHost, tunnelUrl, port)
        fun summary() = "lan=${lanIp.ifEmpty { "-" }} tailscale=${tsIp.ifEmpty { "-" }} public=${publicHost.ifEmpty { "-" }} tunnel=${tunnelUrl.ifEmpty { "-" }}"
    }

    private fun linkMap(): Map<String, Any?>? = Prefs.pcLinkJson?.let { j -> runCatching { Json.parse(j).asMap() }.getOrNull() }

    fun pc(): Pc? {
        val m = linkMap() ?: return null
        val tok = m.string("token"); if (tok.isEmpty()) return null
        val manual = Prefs.pcRouteJson?.let { runCatching { Json.parse(it).asMap() }.getOrNull() } ?: emptyMap()
        // Routes typed on the phone (pc route) win over what the PC sent.
        val pub = manual.string("public_host").ifEmpty { m.string("public_host") }
        val tun = manual.string("tunnel_url").ifEmpty { m.string("tunnel_url") }
        return Pc(tok, m.int("port", 8766), m.string("lan_ip"), m.string("tailscale_ip"), m.string("mac"), m.string("name"), pub, tun)
    }

    fun store(json: String) { Prefs.pcLinkJson = json; cachedBase = null; CmdLog.add("PC link stored: ${pc()?.name} ${pc()?.summary()}") }

    /** Merge route fields the PC sends later (reply to /phone/report, or a re-pair). Only known keys, only non-empty values. */
    fun merge(m: Map<String, Any?>): Boolean {
        val cur = linkMap()?.toMutableMap() ?: return false
        var changed = false
        for (k in listOf("lan_ip", "tailscale_ip", "public_host", "tunnel_url", "mac", "name", "port")) {
            val v = m[k] ?: continue
            if (v is String && v.isEmpty()) continue
            if (cur[k] != v) { cur[k] = v; changed = true }
        }
        if (changed) { Prefs.pcLinkJson = Json.encode(cur); cachedBase = null; CmdLog.add("PC routes updated: ${pc()?.summary()}") }
        return changed
    }

    /** Set (or clear with "") a route by hand: kind = public | tunnel. */
    fun setManualRoute(kind: String, value: String): String {
        val key = when (kind.lowercase()) { "public", "public_host" -> "public_host"; "tunnel", "tunnel_url" -> "tunnel_url"; else -> return "kind must be public or tunnel" }
        val v = value.trim()
        if (v.isNotEmpty() && key == "tunnel_url" && Routes.tunnelBase(v) == null) return "bad tunnel url"
        val cur = Prefs.pcRouteJson?.let { runCatching { Json.parse(it).asMap() }.getOrNull() }?.toMutableMap() ?: HashMap<String, Any?>()
        if (v.isEmpty()) cur.remove(key) else cur[key] = v
        Prefs.pcRouteJson = if (cur.isEmpty()) null else Json.encode(cur); cachedBase = null
        CmdLog.add("manual PC route $key=${v.ifEmpty { "(cleared)" }}")
        return "ok: ${pc()?.summary() ?: "no PC link yet (route kept for after pairing)"}"
    }

    @Volatile private var cachedBase: String? = null
    @Volatile private var cachedAt = 0L

    /** GET base/health: null when it answers 200, else a short reason ("timeout", "HTTP 502", "connect refused" …). */
    private fun probe(base: String, ms: Int): String? = runCatching {
        val c = URL("$base/health").openConnection() as HttpURLConnection
        c.connectTimeout = ms; c.readTimeout = ms
        val code = c.responseCode
        if (code == 200) null else "HTTP $code"
    }.getOrElse { Routes.reason(it, ms) }

    private fun health(base: String, ms: Int): Boolean = probe(base, ms) == null

    @Volatile private var cachedVia = ""

    /** First base URL that answers: LAN → Tailscale → public → tunnel. */
    fun reach(force: Boolean = false): Pair<String, String>? {
        val p = pc() ?: return null
        cachedBase?.let { if (!force && System.currentTimeMillis() - cachedAt < 60_000) return it to cachedVia }
        for (r in p.routes()) {
            if (health(r.base, r.timeoutMs)) { cachedBase = r.base; cachedVia = r.via; cachedAt = System.currentTimeMillis(); return r.base to r.via }
        }
        return null
    }

    /**
     * Every route with whether it answers right now (pc status): all probed at once, never from the cache, and a
     * route that only timed out gets a second try with double the timeout (an idle Tailscale link needs a moment
     * to wake). Shows the reason when down, the time when up, and marks the route calls currently use.
     */
    fun probeAll(): String {
        val p = pc() ?: return "no PC link"
        val rs = p.routes()
        if (rs.isEmpty()) return "no routes known"
        val results = arrayOfNulls<String>(rs.size)
        val threads = rs.mapIndexed { i, r -> Thread {
            val t0 = System.currentTimeMillis()
            var why = probe(r.base, r.timeoutMs)
            if (why?.startsWith("timeout") == true) why = probe(r.base, r.timeoutMs * 2)?.let { "$it (2 tries)" }
            val ms = System.currentTimeMillis() - t0
            results[i] = if (why == null) "up ${ms}ms" else "down: $why"
        }.apply { start() } }
        threads.forEach { it.join() }
        val using = if (cachedBase != null && System.currentTimeMillis() - cachedAt < 60_000) cachedVia else ""
        return rs.mapIndexed { i, r -> "${r.via.padEnd(9)} ${r.base}  ${results[i]}${if (r.via == using) "  (in use)" else ""}" }.joinToString("\n") +
            "\nphone: ${Addresses.summary()}\nlast report: ${reportSummary()}"
    }

    /** One MCP tools/call on the PC. */
    fun call(tool: String, argsJson: String): ToolResult {
        val p = pc() ?: return ToolResult.err("no PC link yet: run `phone pair` on the PC while the phone is on the LAN")
        val (base, _) = reach() ?: return ToolResult.err("PC unreachable (${p.summary()}); try pc wake if it is asleep, or pc route to add a public/tunnel route")
        val body = """{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":${Json.str(tool)},"arguments":$argsJson}}"""
        val reply = runCatching {
            val c = URL("$base/mcp").openConnection() as HttpURLConnection
            c.requestMethod = "POST"; c.doOutput = true; c.connectTimeout = 5000; c.readTimeout = 180_000
            c.setRequestProperty("Authorization", "Bearer ${p.token}"); c.setRequestProperty("Content-Type", "application/json")
            c.outputStream.use { it.write(body.toByteArray()) }
            if (c.responseCode == 401) return ToolResult.err("PC rejected our token: pair again")
            (if (c.responseCode in 200..299) c.inputStream else c.errorStream).bufferedReader().readText()
        }.getOrElse { cachedBase = null; return ToolResult.err("PC call failed: ${it.message}") }
        val r = runCatching { Json.parse(reply).asMap() }.getOrNull() ?: return ToolResult.err("PC: bad reply: ${reply.take(200)}")
        r["error"]?.asMap()?.let { return ToolResult.err("PC: ${it.string("message")}") }
        val res = r["result"].asMap()
        val content = res["content"] as? List<*> ?: emptyList<Any>()
        val text = content.mapNotNull { it.asMap() }.filter { it.string("type") == "text" }.joinToString("\n") { it.string("text") }
        val img = content.mapNotNull { it.asMap() }.firstOrNull { it.string("type") == "image" }
        val isErr = res["isError"] == true
        return if (img != null) ToolResult(text, isErr, img.string("data"), img.string("mimeType", "image/png")) else ToolResult(text, isErr)
    }

    // ---- reporting to the PC (POST /phone/report) ----

    @Volatile var lastReportAt = 0L; private set
    @Volatile var lastReportVia = ""; private set
    @Volatile var lastReportError = ""; private set
    @Volatile var lastReportTry = 0L; private set
    private val reportLock = Any()

    /** "3m ago via tailscale", "never", or "failed 2m ago: timeout 6000ms (last ok 1h ago via lan)". */
    fun reportSummary(): String {
        val now = System.currentTimeMillis()
        fun ago(t: Long) = ((now - t) / 1000).let { s -> if (s < 60) "${s}s" else if (s < 3600) "${s / 60}m" else "${s / 3600}h${(s % 3600) / 60}m" } + " ago"
        val ok = if (lastReportAt > 0) "${ago(lastReportAt)} via $lastReportVia" else "never"
        return if (lastReportError.isEmpty() || lastReportTry <= lastReportAt) ok else "failed ${ago(lastReportTry)}: $lastReportError" + (if (lastReportAt > 0) " (last ok $ok)" else "")
    }

    /**
     * Tell the PC where we are (LAN / Tailscale / adb port / battery / network). Only works once paired.
     * Independent of the shell channel, so it never waits behind a stuck ADB/helper attempt. A failed POST
     * drops the cached route and tries once more over a fresh probe. The PC answers with its current routes,
     * so reporting is also how a changed tunnel URL reaches the phone. Returns null when sent, else the reason.
     */
    fun report(ctx: Context, adbPort: Int = 0, why: String = ""): String? = synchronized(reportLock) {
        val p = pc() ?: return "no PC link"
        lastReportTry = System.currentTimeMillis()
        val a = Addresses.find()
        val bm = ctx.getSystemService(BatteryManager::class.java)
        val bat = bm?.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY) ?: -1
        val body = """{"LanIP":${Json.str(a.lan ?: "")},"TailscaleIP":${Json.str(a.tailscale ?: "")},"AdbSerial":${Json.str(if (adbPort > 0 && a.lan != null) "${a.lan}:$adbPort" else "")},"AdbTcp":${Json.str(if (a.tailscale != null) "${a.tailscale}:${com.qcenaman.mobiletokensaver.adb.AdbManager.TCP_PORT}" else "")},"Battery":"$bat","Network":${Json.str(if (a.lan != null) "wifi" else "mobile")}}"""
        fun post(base: String): String? = runCatching {
            val c = URL("$base/phone/report").openConnection() as HttpURLConnection
            c.requestMethod = "POST"; c.doOutput = true; c.connectTimeout = 6000; c.readTimeout = 10_000
            c.setRequestProperty("Authorization", "Bearer ${p.token}"); c.setRequestProperty("Content-Type", "application/json")
            c.outputStream.use { it.write(body.toByteArray()) }
            val code = c.responseCode
            if (code != 200) return@runCatching "HTTP $code"
            // The PC may answer with its current routes ({"routes":{"tunnel_url":…,"public_host":…}} or the keys at top level).
            val reply = runCatching { c.inputStream.bufferedReader().readText() }.getOrDefault("")
            if (reply.trim().startsWith("{")) runCatching { Json.parse(reply).asMap() }.getOrNull()?.let { r -> merge(r["routes"]?.asMap() ?: r) }
            null
        }.getOrElse { Routes.reason(it, 6000) }
        var route = reach()
        var err = if (route == null) "no route answered" else post(route.first)
        if (err != null) {
            cachedBase = null
            route = reach(force = true)
            err = if (route == null) "no route answered (${p.summary()})" else post(route.first)
        }
        val tag = if (why.isEmpty()) "" else " ($why)"
        if (err == null) {
            lastReportAt = System.currentTimeMillis(); lastReportVia = route!!.second; lastReportError = ""
            CmdLog.add("reported to PC via ${route.second}$tag: lan=${a.lan ?: "-"} ts=${a.tailscale ?: "-"} net=${if (a.lan != null) "wifi" else "mobile"}")
        } else {
            lastReportError = err
            CmdLog.add("report to PC failed$tag: $err")
        }
        err
    }

    private val reporter = java.util.concurrent.Executors.newSingleThreadExecutor { r -> Thread(r, "pc-report").apply { isDaemon = true } }
    @Volatile private var reportQueued = false

    /** Report in the background, coalescing bursts (several network callbacks in a row become one report). */
    fun reportSoon(ctx: Context, why: String, delayMs: Long = 0) {
        if (reportQueued) return
        reportQueued = true
        reporter.execute {
            try { if (delayMs > 0) Thread.sleep(delayMs) } catch (_: InterruptedException) {}
            reportQueued = false
            try { report(ctx, why = why) } catch (_: Exception) {}
        }
    }

    /**
     * Wake-on-LAN: magic packet to the PC's MAC over the local broadcast (home Wi-Fi), and, when a public
     * route is set, to that host too (reaches the PC from anywhere if the router forwards UDP 9 to the LAN broadcast).
     */
    fun wake(): String {
        val p = pc() ?: return "no PC link"
        val mac = p.mac.split(':', '-').mapNotNull { it.toIntOrNull(16)?.toByte() }
        if (mac.size != 6) return "PC MAC unknown (pair again at home)"
        val packet = ByteArray(6) { 0xFF.toByte() } + ByteArray(16 * 6) { mac[it % 6] }
        val targets = listOfNotNull(Addresses.find().lan?.let { broadcastOf(it) }, "255.255.255.255", Routes.publicHostOnly(p.publicHost))
        return runCatching {
            DatagramSocket().use { s -> s.broadcast = true; for (t in targets) for (port in intArrayOf(9, 7)) runCatching { s.send(DatagramPacket(packet, packet.size, InetAddress.getByName(t), port)) } }
            "magic packet sent to ${p.mac} via ${targets.joinToString()}" + if (p.publicHost.isEmpty()) " (only reaches the PC from the home network)" else " (public host only works if the router forwards UDP 9 to the LAN broadcast)"
        }.getOrElse { "wake failed: ${it.message}" }
    }

    private fun broadcastOf(ip: String): String = ip.substringBeforeLast('.') + ".255"
}
