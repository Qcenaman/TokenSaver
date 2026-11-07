package com.qcenaman.mobiletokensaver.auth

import com.qcenaman.mobiletokensaver.mcp.Json
import com.qcenaman.mobiletokensaver.mcp.asMap
import java.io.File
import java.net.URI
import java.net.URLEncoder
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap

/**
 * A tiny OAuth 2.1 authorization server for one client (the user's claude.ai connector):
 * authorization code + PKCE (S256) + refresh tokens. Every sign-in waits for Approve on the phone.
 * The static access token from the app keeps working as a bearer token too.
 * Tokens are stored only as SHA-256 hashes.
 */
class OAuth(
    private val clientId: () -> String,
    private val clientSecret: () -> String,
    private val staticToken: () -> String,
    private val store: File? = null,
    private val onChange: () -> Unit = {},
    private val now: () -> Long = System::currentTimeMillis,
) {
    class Pending(
        val id: String, val clientId: String, val redirectUri: String, val state: String,
        val challenge: String, val scope: String, val resource: String, val at: Long,
    ) {
        @Volatile var approved: Boolean? = null
        @Volatile var code: String? = null
    }

    sealed class Step {
        /** Show the user an error page (nothing safe to redirect to). */
        class Error(val message: String) : Step()
        class Redirect(val url: String) : Step()
        /** Waiting for Approve on the phone. */
        class Wait(val id: String) : Step()
    }

    private class Grant(val clientId: String, val redirectUri: String, val challenge: String, val scope: String, val exp: Long)

    private val pending = ConcurrentHashMap<String, Pending>()
    private val codes = ConcurrentHashMap<String, Grant>()
    private val access = ConcurrentHashMap<String, Long>()
    private val refresh = ConcurrentHashMap<String, Long>()
    private val rnd = SecureRandom()

    init { load() }

    // ---- metadata ----

    fun resourceMetadata(base: String) = Json.encode(mapOf(
        "resource" to "$base/mcp",
        "authorization_servers" to listOf(base),
        "bearer_methods_supported" to listOf("header"),
        "scopes_supported" to listOf("mcp"),
    ))

    fun serverMetadata(base: String) = Json.encode(mapOf(
        "issuer" to base,
        "authorization_endpoint" to "$base/authorize",
        "token_endpoint" to "$base/token",
        "response_types_supported" to listOf("code"),
        "grant_types_supported" to listOf("authorization_code", "refresh_token"),
        "code_challenge_methods_supported" to listOf("S256"),
        "token_endpoint_auth_methods_supported" to listOf("client_secret_post", "client_secret_basic"),
        "scopes_supported" to listOf("mcp"),
    ))

    // ---- /authorize ----

    fun authorize(p: Map<String, String>): Step {
        val cid = p["client_id"].orEmpty()
        val redirect = p["redirect_uri"].orEmpty()
        if (!eq(cid, clientId())) return Step.Error("Unknown client_id. Paste the Client ID shown in the MobileTokenSaver app into the connector's Advanced settings.")
        if (!redirectAllowed(redirect)) return Step.Error("redirect_uri not allowed: $redirect")
        val state = p["state"].orEmpty()
        if (p["response_type"] != "code") return Step.Redirect(withQuery(redirect, "error" to "unsupported_response_type", "state" to state))
        val challenge = p["code_challenge"].orEmpty()
        if (challenge.isEmpty() || p["code_challenge_method"] != "S256")
            return Step.Redirect(withQuery(redirect, "error" to "invalid_request", "error_description" to "PKCE S256 required", "state" to state))
        expire()
        val id = token(12)
        pending[id] = Pending(id, cid, redirect, state, challenge, p["scope"].orEmpty().ifEmpty { "mcp" }, p["resource"].orEmpty(), now())
        onChange()
        return Step.Wait(id)
    }

    /** Polled by the waiting page until the phone answers. */
    fun poll(id: String): Step {
        val pe = pending[id] ?: return Step.Error("This sign-in request expired. Start the connection again from Claude.")
        return when (pe.approved) {
            null -> if (now() - pe.at > PENDING_MS) { pending.remove(id); onChange(); Step.Redirect(withQuery(pe.redirectUri, "error" to "access_denied", "error_description" to "not approved in time", "state" to pe.state)) } else Step.Wait(id)
            false -> { pending.remove(id); onChange(); Step.Redirect(withQuery(pe.redirectUri, "error" to "access_denied", "state" to pe.state)) }
            true -> { pending.remove(id); onChange(); Step.Redirect(withQuery(pe.redirectUri, "code" to pe.code!!, "state" to pe.state)) }
        }
    }

    fun decide(id: String, approve: Boolean): Boolean {
        val pe = pending[id] ?: return false
        if (approve) {
            val c = token(24)
            codes[c] = Grant(pe.clientId, pe.redirectUri, pe.challenge, pe.scope, now() + CODE_MS)
            pe.code = c
        }
        pe.approved = approve
        onChange()
        return true
    }

    fun pendingList(): List<Pending> = pending.values.filter { it.approved == null && now() - it.at <= PENDING_MS }.sortedBy { it.at }

    // ---- /token ----

    /** Returns HTTP status and JSON body. [basic] is the Authorization header value, if any. */
    fun token(form: Map<String, String>, basic: String?): Pair<Int, String> {
        var cid = form["client_id"].orEmpty(); var secret = form["client_secret"].orEmpty()
        if (basic != null && basic.startsWith("Basic ", true)) {
            val dec = runCatching { String(Base64.getDecoder().decode(basic.substring(6).trim())) }.getOrNull()
            if (dec != null && dec.contains(':')) {
                cid = java.net.URLDecoder.decode(dec.substringBefore(':'), "UTF-8")
                secret = java.net.URLDecoder.decode(dec.substringAfter(':'), "UTF-8")
            }
        }
        if (!eq(cid, clientId()) || !eq(secret, clientSecret())) return 401 to err("invalid_client")
        expire()
        return when (form["grant_type"]) {
            "authorization_code" -> {
                val g = codes.remove(form["code"].orEmpty()) ?: return 400 to err("invalid_grant")
                if (g.exp < now() || g.clientId != cid) return 400 to err("invalid_grant")
                if (form["redirect_uri"] != null && form["redirect_uri"] != g.redirectUri) return 400 to err("invalid_grant")
                if (!eq(s256(form["code_verifier"].orEmpty()), g.challenge)) return 400 to err("invalid_grant", "PKCE verification failed")
                200 to issue(g.scope)
            }
            "refresh_token" -> {
                val h = hash(form["refresh_token"].orEmpty())
                val exp = refresh.remove(h) ?: return 400 to err("invalid_grant")
                if (exp < now()) return 400 to err("invalid_grant")
                200 to issue("mcp")
            }
            else -> 400 to err("unsupported_grant_type")
        }
    }

    private fun issue(scope: String): String {
        val a = token(32); val r = token(32)
        access[hash(a)] = now() + ACCESS_MS
        refresh[hash(r)] = now() + REFRESH_MS
        save()
        return Json.encode(mapOf("access_token" to a, "token_type" to "Bearer", "expires_in" to ACCESS_MS / 1000, "refresh_token" to r, "scope" to scope))
    }

    // ---- checks ----

    /** True for the static token or a live OAuth access token. [header] is the raw Authorization value. */
    fun check(header: String?): Boolean {
        val t = header?.trim()?.let { if (it.startsWith("Bearer ", true)) it.substring(7).trim() else it }.orEmpty()
        if (t.isEmpty()) return false
        if (eq(t, staticToken())) return true
        val exp = access[hash(t)] ?: return false
        return exp >= now()
    }

    /** Sign every Claude session out (they must sign in, and be approved, again). */
    fun revokeAll() { access.clear(); refresh.clear(); codes.clear(); save() }

    fun sessions(): Int { expire(); return refresh.size }

    // ---- helpers ----

    private fun expire() {
        val t = now()
        codes.entries.removeIf { it.value.exp < t }
        access.entries.removeIf { it.value < t }
        refresh.entries.removeIf { it.value < t }
        pending.entries.removeIf { t - it.value.at > PENDING_MS * 2 }
    }

    private fun token(n: Int) = ByteArray(n).also { rnd.nextBytes(it) }.let { Base64.getUrlEncoder().withoutPadding().encodeToString(it) }

    private fun err(code: String, desc: String? = null) =
        Json.encode(if (desc == null) mapOf("error" to code) else mapOf("error" to code, "error_description" to desc))

    @Synchronized private fun save() {
        val f = store ?: return
        runCatching { f.parentFile?.mkdirs(); f.writeText(Json.encode(mapOf("a" to HashMap(access), "r" to HashMap(refresh)))) }
    }

    private fun load() {
        val f = store ?: return
        runCatching {
            val m = Json.parse(f.readText()).asMap()
            m["a"].asMap().forEach { (k, v) -> (v as? Number)?.let { access[k] = it.toLong() } }
            m["r"].asMap().forEach { (k, v) -> (v as? Number)?.let { refresh[k] = it.toLong() } }
        }
    }

    companion object {
        const val PENDING_MS = 5 * 60_000L
        const val CODE_MS = 5 * 60_000L
        const val ACCESS_MS = 24 * 3600_000L
        const val REFRESH_MS = 90L * 24 * 3600_000L

        fun s256(verifier: String): String =
            Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(verifier.toByteArray(Charsets.US_ASCII)))

        fun hash(s: String): String = MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).joinToString("") { "%02x".format(it) }

        fun eq(a: String, b: String) = a.isNotEmpty() && MessageDigest.isEqual(a.toByteArray(), b.toByteArray())

        /** claude.ai / claude.com callbacks over https, or a loopback callback (Claude Code / desktop). */
        fun redirectAllowed(u: String): Boolean {
            val uri = runCatching { URI(u) }.getOrNull() ?: return false
            val host = uri.host?.lowercase() ?: return false
            if (uri.rawFragment != null) return false
            return when (uri.scheme?.lowercase()) {
                "https" -> host == "claude.ai" || host == "claude.com" || host.endsWith(".claude.ai") || host.endsWith(".claude.com")
                "http" -> host == "localhost" || host == "127.0.0.1" || host == "[::1]"
                else -> false
            }
        }

        fun withQuery(base: String, vararg kv: Pair<String, String>): String {
            val q = kv.filter { it.second.isNotEmpty() }.joinToString("&") { "${enc(it.first)}=${enc(it.second)}" }
            return if (q.isEmpty()) base else base + (if (base.contains('?')) "&" else "?") + q
        }

        private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")

        /** Parse application/x-www-form-urlencoded (or a query string). */
        fun form(s: String): Map<String, String> = s.split('&').filter { it.isNotEmpty() }.associate {
            val k = it.substringBefore('='); val v = if (it.contains('=')) it.substringAfter('=') else ""
            java.net.URLDecoder.decode(k, "UTF-8") to java.net.URLDecoder.decode(v, "UTF-8")
        }
    }
}
