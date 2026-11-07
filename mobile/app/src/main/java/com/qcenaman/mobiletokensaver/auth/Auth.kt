package com.qcenaman.mobiletokensaver.auth

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.R
import com.qcenaman.mobiletokensaver.service.ActionReceiver
import com.qcenaman.mobiletokensaver.ui.MainActivity
import kotlinx.coroutines.flow.MutableStateFlow
import java.io.File

/** The app's OAuth server and the Approve/Deny notifications for it. */
object Auth {
    lateinit var oauth: OAuth
        private set
    val pending = MutableStateFlow<List<OAuth.Pending>>(emptyList())
    private val shown = HashSet<String>()

    fun init(ctx: Context) {
        oauth = OAuth(
            clientId = { Prefs.clientId }, clientSecret = { Prefs.clientSecret }, staticToken = { Prefs.token },
            store = File(ctx.filesDir, "oauth/tokens.json"), onChange = { changed(ctx.applicationContext) },
        )
    }

    fun decide(ctx: Context, id: String, approve: Boolean) {
        if (oauth.decide(id, approve)) CmdLog.add(if (approve) "Claude sign-in approved" else "Claude sign-in denied")
    }

    private fun changed(ctx: Context) {
        val list = oauth.pendingList()
        pending.value = list
        val nm = ctx.getSystemService(NotificationManager::class.java)
        synchronized(shown) {
            val live = list.map { it.id }.toSet()
            shown.filter { it !in live }.forEach { nm.cancel(it.hashCode()); shown.remove(it) }
            list.filter { it.id !in shown }.forEach { p -> shown.add(p.id); nm.notify(p.id.hashCode(), notification(ctx, p)) }
        }
    }

    private fun notification(ctx: Context, p: OAuth.Pending): Notification {
        fun act(label: String, what: String) = Notification.Action.Builder(null, label,
            PendingIntent.getBroadcast(ctx, (what + p.id).hashCode(),
                Intent(ctx, ActionReceiver::class.java).setAction(what).putExtra("id", p.id),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)).build()
        val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val from = runCatching { java.net.URI(p.redirectUri).host }.getOrNull() ?: p.redirectUri
        return Notification.Builder(ctx, App.CH_PAIR)
            .setSmallIcon(R.drawable.ic_engine)
            .setContentTitle("Claude wants to control this phone")
            .setContentText("Sign-in from $from. Approve only if you just connected it.")
            .setContentIntent(open)
            .setCategory(Notification.CATEGORY_CALL)
            .setAutoCancel(true)
            .addAction(act("Approve", ActionReceiver.APPROVE))
            .addAction(act("Deny", ActionReceiver.DENY))
            .build()
    }
}
