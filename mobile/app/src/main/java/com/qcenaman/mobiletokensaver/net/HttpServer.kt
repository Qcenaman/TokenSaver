package com.qcenaman.mobiletokensaver.net

import com.qcenaman.mobiletokensaver.auth.Auth
import com.qcenaman.mobiletokensaver.auth.OAuth
import com.qcenaman.mobiletokensaver.mcp.McpServer
import com.qcenaman.mobiletokensaver.mcp.asMap
import com.qcenaman.mobiletokensaver.tools.Registry
import fi.iki.elonen.NanoHTTPD

/**
 * MCP over Streamable HTTP at /mcp (POST JSON-RPC; GET/DELETE answer 405 since the server never pushes),
 * plus the OAuth endpoints claude.ai uses. Reached publicly through the Tailscale Funnel tunnel, which sets
 * X-Forwarded-Proto/Host so URLs in the OAuth metadata point at the public address.
 */
class HttpServer(port: Int, private val mcp: McpServer = McpServer(Registry.tools), private val oauth: () -> OAuth = { Auth.oauth }) : NanoHTTPD(port) {

    override fun serve(s: IHTTPSession): Response {
        val r = try { route(s) } catch (e: Throwable) {
            com.qcenaman.mobiletokensaver.CmdLog.add("http ${s.method} ${s.uri} crashed: ${e.javaClass.simpleName}: ${e.message}")
            text(Response.Status.INTERNAL_ERROR, "error: ${e.message}")
        }
        if (s.uri != "/health" && !s.uri.startsWith("/authorize/wait")) {
            val st = r.status?.requestStatus ?: 0
            val why = lastNote.also { lastNote = "" }
            com.qcenaman.mobiletokensaver.CmdLog.add("http ${s.method} ${s.uri} → $st${if (why.isNotEmpty()) " ($why)" else ""}")
        }
        return r
    }

    /** Extra detail for the request log line (why a request was refused). */
    @Volatile private var lastNote = ""

    private fun route(s: IHTTPSession): Response {
        val path = s.uri.trimEnd('/').ifEmpty { "/" }
        return when {
            path == "/health" -> text(Response.Status.OK, "ok")
            path == "/mcp" -> when (s.method) {
                Method.POST -> mcp(s)
                Method.OPTIONS -> cors(newFixedLengthResponse(Response.Status.NO_CONTENT, "text/plain", ""))
                else -> text(Response.Status.METHOD_NOT_ALLOWED, "use POST").also { it.addHeader("Allow", "POST") }
            }
            path.startsWith("/.well-known/oauth-protected-resource") -> json(Response.Status.OK, oauth().resourceMetadata(base(s)))
            path.startsWith("/.well-known/oauth-authorization-server") || path.startsWith("/.well-known/openid-configuration") ->
                json(Response.Status.OK, oauth().serverMetadata(base(s)))
            path == "/authorize" && s.method == Method.GET -> step(oauth().authorize(query(s)))
            path == "/authorize/wait" -> step(oauth().poll(query(s)["id"].orEmpty()))
            path == "/token" && s.method == Method.POST -> {
                val form = OAuth.form(body(s))
                val (code, body) = oauth().token(form, s.headers["authorization"])
                lastNote = "grant=${form["grant_type"]}, auth=${if (s.headers["authorization"] != null) "basic" else if (form.containsKey("client_secret")) "post" else "none"}" +
                    if (code != 200) ", $body" else ""
                json(status(code), body).also { it.addHeader("Cache-Control", "no-store") }
            }
            path == "/link" && s.method == Method.POST -> link(s)
            path == "/" -> text(Response.Status.OK, "MobileTokenSaver: MCP endpoint is /mcp")
            else -> text(Response.Status.NOT_FOUND, "not found")
        }
    }

    private fun mcp(s: IHTTPSession): Response {
        if (!oauth().check(s.headers["authorization"] ?: s.headers["x-mts-token"])) {
            lastNote = if (s.headers["authorization"] == null) "no token" else "token not accepted"
            return json(Response.Status.UNAUTHORIZED, """{"error":"invalid_token"}""").also {
                it.addHeader("WWW-Authenticate", "Bearer resource_metadata=\"${base(s)}/.well-known/oauth-protected-resource\"")
            }
        }
        val b = body(s)
        lastNote = Regex("\"method\"\\s*:\\s*\"([^\"]+)\"").find(b)?.groupValues?.get(1).orEmpty()
        val reply = mcp.handleHttp(b) ?: return newFixedLengthResponse(Response.Status.ACCEPTED, "text/plain", "")
        return json(Response.Status.OK, reply)
    }

    private fun step(st: OAuth.Step): Response = when (st) {
        is OAuth.Step.Redirect -> newFixedLengthResponse(Response.Status.REDIRECT_SEE_OTHER, "text/plain", "").also { it.addHeader("Location", st.url) }
        is OAuth.Step.Error -> page(Response.Status.BAD_REQUEST, "Can't sign in", esc(st.message), refreshTo = null)
        is OAuth.Step.Wait -> page(Response.Status.OK, "Approve on your phone",
            "Tap <b>Approve</b> in the MobileTokenSaver notification on your phone. This page continues by itself.",
            refreshTo = "/authorize/wait?id=${st.id}")
    }

    private fun page(status: Response.Status, title: String, html: String, refreshTo: String?): Response {
        val refresh = if (refreshTo != null) """<meta http-equiv="refresh" content="2;url=$refreshTo">""" else ""
        val body = """<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">$refresh<title>MobileTokenSaver</title></head>""" +
            """<body style="font-family:system-ui,sans-serif;text-align:center;padding:48px 20px;background:#111;color:#eee"><h2>$title</h2><p>$html</p></body></html>"""
        return newFixedLengthResponse(status, "text/html; charset=utf-8", body).also { it.addHeader("Cache-Control", "no-store") }
    }

    private fun esc(s: String) = s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    /** Public origin as the client sees it (the tunnel sets the forwarded headers). */
    private fun base(s: IHTTPSession): String {
        val proto = s.headers["x-forwarded-proto"] ?: "http"
        val host = s.headers["x-forwarded-host"] ?: s.headers["host"] ?: "127.0.0.1"
        return "$proto://$host"
    }

    /**
     * The PC's `phone pair` posts its link details here (token, port, addresses, MAC, name); an already paired PC
     * posts only new routes (e.g. a new tunnel URL). Only the app's own access token is accepted, never an OAuth one.
     */
    private fun link(s: IHTTPSession): Response {
        val auth = s.headers["authorization"]?.removePrefix("Bearer ")?.trim() ?: s.headers["x-mts-token"]
        if (auth.isNullOrEmpty() || auth != com.qcenaman.mobiletokensaver.Prefs.token) { lastNote = "bad token"; return text(Response.Status.UNAUTHORIZED, "unauthorized") }
        if (!com.qcenaman.mobiletokensaver.Prefs.pcLinkOn) return text(Response.Status.FORBIDDEN, "Link to my PC is off in MobileTokenSaver")
        val json = body(s)
        val ctx = com.qcenaman.mobiletokensaver.App.ctx
        if (!json.contains("\"token\"")) {
            val m = runCatching { com.qcenaman.mobiletokensaver.mcp.Json.parse(json).asMap() }.getOrNull()
                ?: return text(Response.Status.BAD_REQUEST, "bad json")
            if (PcLink.pc() == null) return text(Response.Status.BAD_REQUEST, "no PC link yet: POST the full link (with token) to /link")
            PcLink.merge(m["routes"]?.asMap() ?: m)
            PcLink.reportSoon(ctx, "link", 1500)
            return text(Response.Status.OK, "ok")
        }
        PcLink.store(json)
        PcLink.reportSoon(ctx, "pair", 1500)
        return text(Response.Status.OK, "ok")
    }

    private fun query(s: IHTTPSession) = OAuth.form(s.queryParameterString.orEmpty())

    /** Read the body ourselves: NanoHTTPD decodes postData as ASCII. */
    private fun body(s: IHTTPSession): String {
        val n = s.headers["content-length"]?.toIntOrNull() ?: return ""
        if (n <= 0) return ""
        val buf = ByteArray(minOf(n, MAX_BODY)); var off = 0
        val inp = s.inputStream
        while (off < buf.size) { val r = inp.read(buf, off, buf.size - off); if (r < 0) break; off += r }
        return String(buf, 0, off, Charsets.UTF_8)
    }

    private fun status(code: Int) = when (code) {
        200 -> Response.Status.OK; 401 -> Response.Status.UNAUTHORIZED; else -> Response.Status.BAD_REQUEST
    }

    private fun json(st: Response.Status, body: String) = newFixedLengthResponse(st, "application/json; charset=utf-8", body)
    private fun text(st: Response.Status, body: String) = newFixedLengthResponse(st, "text/plain; charset=utf-8", body)
    private fun cors(r: Response) = r.also {
        it.addHeader("Allow", "POST, OPTIONS")
    }

    companion object { const val MAX_BODY = 32 * 1024 * 1024 }
}
