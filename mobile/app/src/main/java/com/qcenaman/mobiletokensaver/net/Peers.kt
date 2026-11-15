package com.qcenaman.mobiletokensaver.net

import android.provider.Settings
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.Json
import com.qcenaman.mobiletokensaver.mcp.ToolResult
import com.qcenaman.mobiletokensaver.mcp.asMap
import com.qcenaman.mobiletokensaver.mcp.string
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.URL
import java.security.MessageDigest

/**
 * The shared peer registry on this device: every other TokenSaver / MobileTokenSaver node with its link, synced
 * through POST /peers/sync (Bearer = the receiver's own token) with every node every 10 minutes and as soon as this
 * device's own addresses change. The peer tool reaches a node over LAN → Tailscale → tunnel → relay through another
 * node → Wake-on-LAN (here and from the relays) with retries. Tokens never go to the log or tool output.
 */
object Peers {
    private val lock = Any()
    private const val R = PeerRegistry.UNREACHABLE

    fun all(): MutableMap<String, Map<String, Any?>> = synchronized(lock) {
        val m = LinkedHashMap<String, Map<String, Any?>>()
        Prefs.peersJson?.let { j -> runCatching { Json.parse(j).asMap() }.getOrNull() }?.forEach { (k, v) -> m[k] = v.asMap() }
        m
    }

    private fun mergeIn(incoming: Map<String, Any?>): List<String> = synchronized(lock) {
        val m = all()
        val ch = PeerRegistry.merge(m, incoming, selfName())
        if (ch.isNotEmpty()) { Prefs.peersJson = Json.encode(m); CmdLog.add("peers updated: ${ch.joinToString(" ")}"); refreshPcLink(m, ch) }
        ch
    }

    fun selfName(): String = Prefs.meshName.ifEmpty {
        val n = runCatching { Settings.Global.getString(App.ctx.contentResolver, "device_name") }.getOrNull().orEmpty()
        PeerRegistry.norm(n).replace(Regex("[^a-z0-9-]+"), "-").trim('-').ifEmpty { "android" }
    }

    /** This device's own entry; its "updated" time moves only when the link itself changes. */
    fun self(): Map<String, Any?> {
        val a = Addresses.find()
        val name = selfName()
        val fpSrc = listOf(name, a.tailscale.orEmpty(), a.lan.orEmpty(), Prefs.port.toString(), Prefs.token).joinToString("|")
        val fp = MessageDigest.getInstance("SHA-256").digest(fpSrc.toByteArray()).take(8).joinToString("") { "%02x".format(it) }
        if (fp != Prefs.meshFp) { Prefs.meshFp = fp; Prefs.meshUpdated = System.currentTimeMillis() }
        val t = PeerRegistry.goTime(Prefs.meshUpdated)
        return linkedMapOf("ip" to a.tailscale.orEmpty(), "lan" to a.lan.orEmpty(), "port" to Prefs.port, "token" to Prefs.token,
            "kind" to "android", "paired" to t, "updated" to t)
    }

    private fun payload(you: String) = Json.encode(linkedMapOf("from" to selfName(), "self" to self(), "peers" to all(),
        "you" to you, "primary" to Prefs.meshPrimary, "pc" to (PcLink.pc()?.name ?: "")))

    /** POST /peers/sync from another node (already authenticated with our own token): merge, answer with ours. */
    fun handleSync(body: String): String {
        val r = Json.parse(body).asMap()
        val from = PeerRegistry.norm(r.string("from"))
        val incoming = LinkedHashMap<String, Any?>(r["peers"].asMap())
        val selfE = r["self"].asMap()
        if (from.isNotEmpty() && selfE.string("token").isNotEmpty()) incoming[from] = selfE
        takeHints(r, from, selfE)
        val ch = mergeIn(incoming)
        CmdLog.add("peer sync from ${from.ifEmpty { "?" }}: ${ch.size} updated")
        return payload(from)
    }

    /** Names the computers give us, and the primary computer's link for the pc tool when we have none yet. */
    private fun takeHints(r: Map<String, Any?>, from: String, selfE: Map<String, Any?>) {
        val prim = PeerRegistry.norm(r.string("primary"))
        if (prim.isNotEmpty() && prim != Prefs.meshPrimary) Prefs.meshPrimary = prim
        val you = PeerRegistry.norm(r.string("you"))
        if (you.isNotEmpty() && selfE.string("kind") == "computer" && (Prefs.meshName.isEmpty() || from == prim) && you != Prefs.meshName) {
            Prefs.meshName = you; CmdLog.add("registry name: $you")
        }
        if (Prefs.pcLinkOn && PcLink.pc() == null && from.isNotEmpty() && from == prim && selfE.string("kind") == "computer") {
            PcLink.store(Json.encode(linkedMapOf("token" to selfE.string("token"), "port" to PeerRegistry.port(selfE),
                "lan_ip" to selfE.string("lan"), "tailscale_ip" to selfE.string("ip"), "mac" to selfE.string("mac"),
                "name" to from, "tunnel_url" to selfE.string("tunnel"))))
        }
    }

    /** A newer token or address for the PC the pc tool uses goes into its link too. */
    private fun refreshPcLink(m: Map<String, Map<String, Any?>>, changed: List<String>) {
        val pc = PcLink.pc() ?: return
        val name = changed.firstOrNull { it == PeerRegistry.norm(pc.name) || (m[it]?.string("ip")?.isNotEmpty() == true && m[it]?.string("ip") == pc.tsIp) } ?: return
        val e = m[name] ?: return
        PcLink.replaceToken(e.string("token"))
        PcLink.merge(linkedMapOf("lan_ip" to e.string("lan"), "tailscale_ip" to e.string("ip"), "mac" to e.string("mac"), "tunnel_url" to e.string("tunnel")))
    }

    // ---- HTTP ----

    private fun probe(base: String, ms: Int): String? = runCatching {
        val c = URL("$base/health").openConnection() as HttpURLConnection
        c.connectTimeout = ms; c.readTimeout = ms
        val code = c.responseCode
        if (code == 200) null else "HTTP $code"
    }.getOrElse { Routes.reason(it, ms) }

    /** First route that answers: base, via; or null and what each route said. */
    fun reach(e: Map<String, Any?>): Pair<Pair<String, String>?, List<String>> {
        val tried = ArrayList<String>()
        for (r in PeerRegistry.routes(e)) {
            var why = probe(r.base, r.timeoutMs)
            if (why?.startsWith("timeout") == true && r.via == "tailscale") why = probe(r.base, r.timeoutMs * 2)
            if (why == null) return (r.base to r.via) to tried
            tried += "${r.via} $why"
        }
        if (tried.isEmpty()) tried += "no address known"
        return null to tried
    }

    private fun post(base: String, path: String, token: String, body: String, readMs: Int): Pair<Int, String> = runCatching {
        val c = URL("$base$path").openConnection() as HttpURLConnection
        c.requestMethod = "POST"; c.doOutput = true; c.connectTimeout = 6000; c.readTimeout = readMs
        c.setRequestProperty("Authorization", "Bearer $token"); c.setRequestProperty("Content-Type", "application/json")
        c.outputStream.use { it.write(body.toByteArray()) }
        val code = c.responseCode
        code to runCatching { (if (code in 200..299) c.inputStream else c.errorStream)?.bufferedReader()?.readText().orEmpty() }.getOrDefault("")
    }.getOrElse { 0 to (it.message ?: it.javaClass.simpleName) }

    /** One MCP tools/call at base. A null result means the connection itself failed. */
    private fun mcp(base: String, token: String, tool: String, argsJson: String): Pair<Int, ToolResult?> {
        val body = """{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":${Json.str(tool)},"arguments":$argsJson}}"""
        val (code, reply) = post(base, "/mcp", token, body, 180_000)
        if (code == 0) return 0 to null
        if (code == 401) return 401 to ToolResult.err("token rejected")
        val r = runCatching { Json.parse(reply).asMap() }.getOrNull() ?: return code to ToolResult.err("bad reply (HTTP $code): ${reply.take(200)}")
        r["error"]?.asMap()?.let { return code to ToolResult.err(it.string("message")) }
        val res = r["result"].asMap()
        val content = res["content"] as? List<*> ?: emptyList<Any>()
        val text = content.mapNotNull { it.asMap() }.filter { it.string("type") == "text" }.joinToString("\n") { it.string("text") }
        val img = content.mapNotNull { it.asMap() }.firstOrNull { it.string("type") == "image" }
        val isErr = res["isError"] == true
        return code to (if (img != null) ToolResult(text, isErr, img.string("data"), img.string("mimeType", "image/png")) else ToolResult(text, isErr))
    }

    // ---- syncing ----

    fun syncWith(name: String, e: Map<String, Any?>): String {
        val (hit, tried) = reach(e)
        val base = hit?.first ?: return "unreachable (${tried.joinToString(", ")})"
        val (code, reply) = post(base, "/peers/sync", e.string("token"), payload(name), 15_000)
        return when (code) {
            200 -> {
                val r = runCatching { Json.parse(reply).asMap() }.getOrNull() ?: return "bad sync reply"
                val from = PeerRegistry.norm(r.string("from"))
                val incoming = LinkedHashMap<String, Any?>(r["peers"].asMap())
                val selfE = r["self"].asMap()
                if (from.isNotEmpty() && selfE.string("token").isNotEmpty()) incoming[from] = selfE
                takeHints(r, from, selfE)
                "synced (${mergeIn(incoming).size} updated)"
            }
            0 -> "unreachable ($reply)"
            401 -> "token rejected (it gets ours at its next sync)"
            404 -> "no registry sync there yet (update it)"
            else -> "HTTP $code"
        }
    }

    fun syncAll(): String {
        importLinkFiles()
        val m = all()
        if (m.isEmpty()) return "registry empty: a computer adds this device at its next sync (or run peer adopt on it)"
        val results = arrayOfNulls<String>(m.size)
        m.entries.mapIndexed { i, (n, e) -> Thread { results[i] = "$n: ${runCatching { syncWith(n, e) }.getOrElse { "failed: ${it.message}" }}" }.apply { start() } }
            .forEach { it.join() }
        return results.joinToString("\n")
    }

    @Volatile private var lastSync = 0L
    @Volatile private var lastFp = ""

    /** From the reporter loop: every 10 minutes, and at once when this device's own link changed. */
    fun tick() {
        self()
        val now = System.currentTimeMillis()
        if (Prefs.meshFp != lastFp || now - lastSync >= 10 * 60_000L) {
            lastFp = Prefs.meshFp; lastSync = now
            runCatching { syncAll() }
        }
    }

    @Volatile private var imported = false

    /** One-time import of the old hand-made link files in /data/local/tmp (laptop-tokensaver.json …). */
    fun importLinkFiles(): List<String> {
        if (imported) return emptyList()
        imported = true
        val (c, list) = runCatching { Shell.run(App.ctx, "ls /data/local/tmp/*-tokensaver.json 2>/dev/null") }.getOrElse { return emptyList() }
        if (c != 0) return emptyList()
        val have = all()
        val out = ArrayList<String>()
        for (f in list.lines().map { it.trim() }.filter { it.endsWith(".json") }) {
            val read = runCatching { Shell.run(App.ctx, "cat '$f'") }.getOrNull()
            if (read == null || read.first != 0) continue
            val parsed = runCatching { Json.parse(read.second.trim()).asMap() }.getOrNull() ?: continue
            val (name, e) = PeerRegistry.fromLinkFile(f, parsed) ?: continue
            if (have.values.any { it.string("token") == e.string("token") || (e.string("ip").isNotEmpty() && it.string("ip") == e.string("ip")) }) continue
            mergeIn(mapOf(name to e)); out += name
        }
        if (out.isNotEmpty()) CmdLog.add("imported old link files: ${out.joinToString(" ")}")
        return out
    }

    // ---- calling ----

    fun lookup(name: String): Pair<String, Map<String, Any?>>? {
        val n = PeerRegistry.norm(name)
        val m = all()
        m[n]?.let { return n to it }
        return m.entries.firstOrNull { it.value.string("ip") == n || it.value.string("lan") == n }?.let { it.key to it.value }
    }

    private val backoff = longArrayOf(5_000, 10_000, 15_000, 20_000, 20_000, 20_000)

    /** Run tool on the node: direct, else relay (hops 0), else wake + retry (hops 0). */
    fun call(name: String, tool: String, argsJson: String, hops: Int = 0): ToolResult {
        val (n, e) = lookup(name) ?: return ToolResult.err("$R$name is not in this node's registry (peer status lists who is; peer sync asks the others)")
        val notes = ArrayList<String>()
        fun direct(suffix: String): ToolResult? {
            val (hit, tried) = reach(e)
            if (hit == null) { if (suffix.isEmpty()) notes += tried; return null }
            val (code, res) = mcp(hit.first, e.string("token"), tool, argsJson)
            if (res == null) { notes += "${hit.second} dropped"; return null }
            if (code == 401) return ToolResult.err("$n rejected the stored token (it sends a new one at its next sync; peer sync asks now)")
            return if (hops == 0 && hit.second != "lan" && hit.second != "tailscale") ToolResult(res.text + "\n(reached via ${hit.second}$suffix)", res.isError, res.imageB64, res.mime) else res
        }
        direct("")?.let { return it }
        if (hops == 0) {
            for (r in PeerRegistry.relays(n, selfName(), all(), Prefs.meshPrimary)) {
                val res = viaRelay(r, n, tool, argsJson)
                if (!res.isError || !PeerRegistry.relayUnable(res.text)) return ToolResult(res.text + "\n(reached via relay $r)", res.isError, res.imageB64, res.mime)
                notes += "relay $r no"
            }
            if (e.string("mac").isNotEmpty()) {
                notes += wake(n, e, 0)
                for (d in backoff) {
                    try { Thread.sleep(d) } catch (_: InterruptedException) { break }
                    if (reach(e).first != null) direct(" after wake")?.let { return it }
                }
                notes += "still down ~90 s after the wake"
            }
        }
        return ToolResult.err("$R$n: ${notes.joinToString("; ")}")
    }

    fun viaRelay(relay: String, target: String, tool: String, argsJson: String): ToolResult {
        val (rn, r) = lookup(relay) ?: return ToolResult.err("$R$relay unknown")
        val base = reach(r).first?.first ?: return ToolResult.err("$R$rn itself unreachable")
        val a = """{"action":"call","peer":${Json.str(target)},"tool":${Json.str(tool)},"args":$argsJson,"hops":1}"""
        return mcp(base, r.string("token"), "peer", a).second ?: ToolResult.err("$R$rn dropped")
    }

    /** Magic packet from here (home Wi-Fi broadcast) and, for hops 0, from every relay that answers. */
    fun wake(n: String, e: Map<String, Any?>, hops: Int): String {
        val parts = ArrayList<String>()
        val mac = e.string("mac")
        if (mac.isEmpty()) return "$n: MAC unknown (it announces it at its next sync)"
        parts += sendMagic(mac, e.string("lan"))?.let { "here: $it" } ?: "here"
        if (hops == 0) {
            val ok = java.util.Collections.synchronizedList(ArrayList<String>())
            PeerRegistry.relays(n, selfName(), all(), Prefs.meshPrimary).map { r -> Thread {
                val re = lookup(r)?.second ?: return@Thread
                val base = reach(re).first?.first ?: return@Thread
                val res = mcp(base, re.string("token"), "peer", """{"action":"wake","peer":${Json.str(n)},"hops":1}""").second
                if (res != null && !res.isError) ok += r
            }.apply { start() } }.forEach { it.join(30_000) }
            parts += ok
        }
        return "magic packet for $n sent from ${parts.joinToString(", ")}"
    }

    /** null when sent, else why not. */
    fun sendMagic(macStr: String, lan: String): String? {
        val mac = macStr.split(':', '-').mapNotNull { it.toIntOrNull(16)?.toByte() }
        if (mac.size != 6) return "bad MAC"
        val packet = ByteArray(6) { 0xFF.toByte() } + ByteArray(16 * 6) { mac[it % 6] }
        val targets = LinkedHashSet<String>()
        Addresses.find().lan?.let { targets += it.substringBeforeLast('.') + ".255" }
        if (lan.count { it == '.' } == 3) targets += lan.substringBeforeLast('.') + ".255"
        targets += "255.255.255.255"
        return runCatching {
            DatagramSocket().use { s -> s.broadcast = true; for (t in targets) for (p in intArrayOf(9, 7)) runCatching { s.send(DatagramPacket(packet, packet.size, InetAddress.getByName(t), p)) } }
            null
        }.getOrElse { it.message ?: "send failed" }
    }

    /** Every node with each route probed (all at once); tokens never shown. */
    fun status(): String {
        val m = all()
        val lines = arrayOfNulls<String>(m.size)
        m.entries.mapIndexed { i, (n, e) -> Thread {
            val rs = PeerRegistry.routes(e).joinToString(", ") { r -> "${r.via} ${probe(r.base, r.timeoutMs) ?: "up"}" }.ifEmpty { "no address" }
            lines[i] = "  $n (${e.string("kind", "computer")}): $rs${if (e.string("mac").isNotEmpty()) ", wake ok" else ""}"
        }.apply { start() } }.forEach { it.join() }
        val a = Addresses.find()
        return "this device: ${selfName()} (lan ${a.lan ?: "-"}, tailscale ${a.tailscale ?: "-"}), primary ${Prefs.meshPrimary.ifEmpty { "-" }}, pc ${PcLink.pc()?.name ?: "-"}\n" +
            "registry, ${m.size} nodes:\n" + lines.joinToString("\n")
    }
}
