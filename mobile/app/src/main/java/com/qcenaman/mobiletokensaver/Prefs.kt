package com.qcenaman.mobiletokensaver

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import java.security.SecureRandom

/** Settings and the access token, kept in EncryptedSharedPreferences. Call init() once from App. */
object Prefs {
    private lateinit var sp: SharedPreferences

    fun init(ctx: Context) {
        tokenFile = java.io.File(ctx.filesDir, "token")
        sp = try {
            EncryptedSharedPreferences.create(
                ctx, "secure",
                MasterKey.Builder(ctx).setKeyScheme(MasterKey.KeyScheme.AES256_GCM).build(),
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        } catch (e: Exception) {
            ctx.getSharedPreferences("plain_fallback", Context.MODE_PRIVATE)
        }
        token
    }

    private lateinit var tokenFile: java.io.File

    val token: String
        get() = sp.getString("token", null)?.also { mirror(it) } ?: newToken()

    fun newToken(): String {
        val b = ByteArray(24).also { SecureRandom().nextBytes(it) }
        val t = b.joinToString("") { "%02x".format(it) }
        sp.edit().putString("token", t).apply()
        mirror(t)
        return t
    }

    /** Private copy for `adb shell run-as` (adb already outranks the token). */
    private fun mirror(t: String) {
        if (!::tokenFile.isInitialized) return
        if (!tokenFile.exists() || tokenFile.readText() != t) runCatching { tokenFile.writeText(t) }
    }

    var paused: Boolean
        get() = sp.getBoolean("paused", false)
        set(v) = sp.edit().putBoolean("paused", v).apply()
    var killed: Boolean
        get() = sp.getBoolean("killed", false)
        set(v) = sp.edit().putBoolean("killed", v).apply()
    var stayAwake: Boolean
        get() = sp.getBoolean("stay_awake", true)
        set(v) = sp.edit().putBoolean("stay_awake", v).apply()
    var port: Int
        get() = sp.getInt("port", 8765)
        set(v) = sp.edit().putInt("port", v).apply()
    var setupDone: Boolean
        get() = sp.getBoolean("setup_done", false)
        set(v) = sp.edit().putBoolean("setup_done", v).apply()

    /** Secret between the app and its shell helper; never shown, never rotated with the access token. */
    val helperSecret: String
        get() = sp.getString("helper_secret", null) ?: rnd(24).also { sp.edit().putString("helper_secret", it).apply() }

    /** OAuth client credentials the user pastes into claude.ai (Advanced settings of the custom connector). */
    val clientId: String
        get() = sp.getString("client_id", null) ?: ("mts-" + rnd(8)).also { sp.edit().putString("client_id", it).apply() }
    val clientSecret: String
        get() = sp.getString("client_secret", null) ?: newClientSecret()
    fun newClientSecret(): String = rnd(32).also { sp.edit().putString("client_secret", it).apply() }

    var tunnelOn: Boolean
        get() = sp.getBoolean("tunnel_on", true)
        set(v) = sp.edit().putBoolean("tunnel_on", v).apply()
    /** Run the Funnel node as the shell user (via the helper) instead of inside the app. */
    var tunnelAsShell: Boolean
        get() = sp.getBoolean("tunnel_shell", false)
        set(v) = sp.edit().putBoolean("tunnel_shell", v).apply()
    var tsHostname: String
        get() = sp.getString("ts_hostname", null) ?: "mobiletokensaver"
        set(v) = sp.edit().putString("ts_hostname", v).apply()

    /** Link to the user's PC (desktop TokenSaver): the phone reports its addresses and offers the pc tool. */
    var pcLinkOn: Boolean
        get() = sp.getBoolean("pc_link_on", true)
        set(v) = sp.edit().putBoolean("pc_link_on", v).apply()
    /** What the PC's `phone pair` sent (token, port, addresses, MAC, name). */
    var pcLinkJson: String?
        get() = sp.getString("pc_link", null)
        set(v) = sp.edit().putString("pc_link", v).apply()
    /** Routes to the PC set by hand (pc route). */
    var pcRouteJson: String?
        get() = sp.getString("pc_route", null)
        set(v) = sp.edit().putString("pc_route", v).apply()

    private fun rnd(n: Int): String = ByteArray(n).also { SecureRandom().nextBytes(it) }.joinToString("") { "%02x".format(it) }

    fun toolEnabled(name: String) = sp.getBoolean("tool_$name", true)
    fun setToolEnabled(name: String, v: Boolean) = sp.edit().putBoolean("tool_$name", v).apply()
}
