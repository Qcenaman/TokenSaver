package com.qcenaman.mobiletokensaver.mcp

import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.appVersion

/** Dispatches one JSON-RPC line and returns the reply line (or null for notifications). */
class McpServer(private val tools: List<Tool>, private val gate: Gate = Gate()) {
    /** Hooks into app state; the default uses Prefs/CmdLog, tests pass their own. */
    class Gate(
        val enabled: (String) -> Boolean = { Prefs.toolEnabled(it) },
        val paused: () -> Boolean = { Prefs.paused },
        val log: (String) -> Unit = { CmdLog.add(it) }
    )
    private val byName = tools.associateBy { it.name }

    val instructions = "MobileTokenSaver runs on the user's Android phone and gives you its shell (as the shell user), files, apps, screen and notifications. " +
        "Use it for anything on the phone. run for shell commands (cmd, pm, appops, settings, dumpsys, am …; returns exit code and a short tail); " +
        "run_job for several commands; job to follow long ones. scan/find/read/write for files (write also moves, copies, deletes, makes folders). " +
        "apps to list/launch/stop/install/uninstall and grant/revoke permissions or set appops. " +
        "ui to read the screen as a compact text tree and tap/swipe/type; screen for a screenshot only when asked. " +
        "notifications, system, web as named; pc to use the user's desktop PC through its TokenSaver (when linked). Outputs are capped; ask for a tail/limit when you need less."

    /**
     * One Streamable HTTP POST body: a single JSON-RPC message or a batch. Returns the reply body,
     * or null when nothing needs an answer (only notifications/responses: the caller sends 202).
     */
    fun handleHttp(body: String): String? {
        val t = body.trimStart()
        if (!t.startsWith("[")) return handle(t)
        val items = try { Json.parse(t) as? List<*> } catch (e: Exception) { null } ?: return error(null, -32700, "parse error")
        val replies = items.mapNotNull { handle(Json.encode(it)) }
        return if (replies.isEmpty()) null else replies.joinToString(",", "[", "]")
    }

    fun handle(line: String): String? {
        val req = try { Json.parse(line).asMap() } catch (e: Exception) { return error(null, -32700, "parse error") }
        val id = req["id"]
        val method = req.string("method")
        if (id == null) return null // notification
        if (method.isEmpty() && (req.containsKey("result") || req.containsKey("error"))) return null // a reply from the client
        return try {
            when (method) {
                "initialize" -> result(id, """{"protocolVersion":${Json.str(protoVer(req))},"capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"mts","version":${Json.str(appVersion)}},"instructions":${Json.str(instructions)}}""")
                "ping" -> result(id, "{}")
                "tools/list" -> result(id, """{"tools":[${tools.filter { gate.enabled(it.name) }.joinToString(",") { toolJson(it) }}]}""")
                "tools/call" -> callTool(id, req["params"].asMap())
                else -> error(id, -32601, "method not found: $method")
            }
        } catch (e: Throwable) {
            error(id, -32603, "internal error: ${e.message}")
        }
    }

    private fun protoVer(req: Map<String, Any?>): String =
        req["params"].asMap().string("protocolVersion", "2025-06-18").ifEmpty { "2025-06-18" }

    private fun toolJson(t: Tool) =
        """{"name":${Json.str(t.name)},"description":${Json.str(t.description)},"inputSchema":${t.schema}}"""

    private fun callTool(id: Any?, params: Map<String, Any?>): String {
        val name = params.string("name")
        val args = params["arguments"].asMap()
        val tool = byName[name] ?: return error(id, -32602, "unknown tool: $name")
        if (!gate.enabled(name)) return callResult(id, ToolResult.err("tool '$name' is turned off in MobileTokenSaver"))
        if (gate.paused()) return callResult(id, ToolResult.err("MobileTokenSaver is paused"))
        gate.log("$name ${argsPreview(args)}")
        val res = try { tool.handler(args) } catch (e: Throwable) { ToolResult.err("${e.javaClass.simpleName}: ${e.message}") }
        return callResult(id, ToolResult(cap(res.text), res.isError, res.imageB64, res.mime))
    }

    /** Keep huge outputs small: the head and the tail, with how much was cut. */
    fun cap(t: String, max: Int = MAX_TEXT): String {
        if (t.length <= max) return t
        val head = max * 2 / 3; val tail = max - head
        return t.take(head) + "\n… [${t.length - max} chars cut — use tail/limit/lines to see less] …\n" + t.takeLast(tail)
    }

    companion object { const val MAX_TEXT = 20_000 }

    private fun argsPreview(args: Map<String, Any?>): String =
        args.entries.joinToString(" ") { (k, v) -> "$k=${v.toString().take(60)}" }.take(160)

    private fun callResult(id: Any?, r: ToolResult): String {
        val parts = ArrayList<String>()
        if (r.imageB64 != null) parts.add("""{"type":"image","data":${Json.str(r.imageB64)},"mimeType":${Json.str(r.mime)}}""")
        if (r.imageB64 == null || r.text.isNotEmpty()) parts.add("""{"type":"text","text":${Json.str(r.text)}}""")
        return result(id, """{"content":[${parts.joinToString(",")}],"isError":${r.isError}}""")
    }

    private fun result(id: Any?, raw: String) = """{"jsonrpc":"2.0","id":${idJson(id)},"result":$raw}"""
    private fun error(id: Any?, code: Int, msg: String) =
        """{"jsonrpc":"2.0","id":${idJson(id)},"error":{"code":$code,"message":${Json.str(msg)}}}"""
    private fun idJson(id: Any?): String = when (id) {
        is String -> Json.str(id); is Number -> id.toString().removeSuffix(".0"); else -> "null"
    }
}
