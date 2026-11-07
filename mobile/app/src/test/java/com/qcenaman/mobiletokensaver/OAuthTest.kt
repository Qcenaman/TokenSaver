package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.auth.OAuth
import com.qcenaman.mobiletokensaver.mcp.Json
import com.qcenaman.mobiletokensaver.mcp.asMap
import com.qcenaman.mobiletokensaver.mcp.string
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64

class OAuthTest {
    private var clock = 1_000_000L
    private val o = OAuth({ "cid" }, { "sec" }, { "static" }, now = { clock })
    private val redirect = "https://claude.ai/api/mcp/auth_callback"
    private val verifier = "v".repeat(50)

    private fun start(): String {
        val st = o.authorize(mapOf("response_type" to "code", "client_id" to "cid", "redirect_uri" to redirect, "state" to "s1",
            "code_challenge" to OAuth.s256(verifier), "code_challenge_method" to "S256"))
        return (st as OAuth.Step.Wait).id
    }

    private fun codeFrom(url: String) = OAuth.form(url.substringAfter('?'))

    @Test fun fullFlowNeedsApprovalThenGivesWorkingTokens() {
        val id = start()
        assertTrue(o.poll(id) is OAuth.Step.Wait)
        assertEquals(1, o.pendingList().size)
        o.decide(id, true)
        val q = codeFrom((o.poll(id) as OAuth.Step.Redirect).url)
        assertEquals("s1", q["state"])
        val (st, body) = o.token(mapOf("grant_type" to "authorization_code", "code" to q["code"]!!, "redirect_uri" to redirect,
            "code_verifier" to verifier, "client_id" to "cid", "client_secret" to "sec"), null)
        assertEquals(200, st)
        val tok = Json.parse(body).asMap()
        assertTrue(o.check("Bearer " + tok.string("access_token")))
        // code is single use
        assertEquals(400, o.token(mapOf("grant_type" to "authorization_code", "code" to q["code"]!!, "code_verifier" to verifier), "Basic " + b64("cid:sec")).first)
        // refresh with basic auth rotates the refresh token
        val (rs, rb) = o.token(mapOf("grant_type" to "refresh_token", "refresh_token" to tok.string("refresh_token")), "Basic " + b64("cid:sec"))
        assertEquals(200, rs)
        assertTrue(o.check("Bearer " + Json.parse(rb).asMap().string("access_token")))
        assertEquals(400, o.token(mapOf("grant_type" to "refresh_token", "refresh_token" to tok.string("refresh_token"), "client_id" to "cid", "client_secret" to "sec"), null).first)
        // access tokens expire
        clock += OAuth.ACCESS_MS + 1
        assertFalse(o.check("Bearer " + tok.string("access_token")))
        o.revokeAll()
        assertEquals(0, o.sessions())
    }

    @Test fun denyWrongSecretAndBadPkce() {
        val id = start(); o.decide(id, false)
        assertTrue((o.poll(id) as OAuth.Step.Redirect).url.contains("error=access_denied"))
        val id2 = start(); o.decide(id2, true)
        val code = codeFrom((o.poll(id2) as OAuth.Step.Redirect).url)["code"]!!
        assertEquals(401, o.token(mapOf("grant_type" to "authorization_code", "code" to code, "code_verifier" to verifier, "client_id" to "cid", "client_secret" to "nope"), null).first)
        assertEquals(400, o.token(mapOf("grant_type" to "authorization_code", "code" to code, "code_verifier" to "wrong", "client_id" to "cid", "client_secret" to "sec"), null).first)
    }

    @Test fun rejectsUnknownClientsAndRedirects() {
        assertTrue(o.authorize(mapOf("client_id" to "x", "redirect_uri" to redirect)) is OAuth.Step.Error)
        assertTrue(o.authorize(mapOf("client_id" to "cid", "redirect_uri" to "https://evil.example/cb")) is OAuth.Step.Error)
        assertTrue(OAuth.redirectAllowed("https://claude.com/api/mcp/auth_callback"))
        assertTrue(OAuth.redirectAllowed("http://localhost:33418/callback"))
        assertFalse(OAuth.redirectAllowed("https://claude.ai.evil.com/cb"))
        assertFalse(OAuth.redirectAllowed("http://claude.ai/cb"))
        // missing PKCE goes back to Claude as an error
        val st = o.authorize(mapOf("response_type" to "code", "client_id" to "cid", "redirect_uri" to redirect, "state" to "z"))
        assertTrue((st as OAuth.Step.Redirect).url.contains("error=invalid_request"))
    }

    @Test fun staticTokenAndPendingExpiry() {
        assertTrue(o.check("Bearer static"))
        assertTrue(o.check("static"))
        assertFalse(o.check(null))
        assertFalse(o.check("Bearer "))
        val id = start()
        clock += OAuth.PENDING_MS + 1
        assertTrue((o.poll(id) as OAuth.Step.Redirect).url.contains("error=access_denied"))
        assertTrue(o.pendingList().isEmpty())
    }

    @Test fun metadataPointsAtBase() {
        val m = Json.parse(o.serverMetadata("https://p.ts.net")).asMap()
        assertEquals("https://p.ts.net/token", m.string("token_endpoint"))
        assertEquals("https://p.ts.net/mcp", Json.parse(o.resourceMetadata("https://p.ts.net")).asMap().string("resource"))
    }

    private fun b64(s: String) = Base64.getEncoder().encodeToString(s.toByteArray())
}
