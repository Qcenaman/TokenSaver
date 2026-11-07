package com.qcenaman.mobiletokensaver.service

import android.app.RemoteInput
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.adb.Pairing

/** Handles Pause / Kill, Approve / Deny for Claude sign-ins, and the inline pairing-code reply on notifications. */
class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        val app = context.applicationContext
        when (intent?.action) {
            PAUSE -> { Prefs.paused = !Prefs.paused; CmdLog.add(if (Prefs.paused) "paused" else "resumed"); EngineService.start(app) }
            KILL -> {
                Prefs.killed = true; CmdLog.add("killed from notification")
                context.stopService(Intent(context, EngineService::class.java))
            }
            APPROVE, DENY -> intent.getStringExtra("id")?.let { com.qcenaman.mobiletokensaver.auth.Auth.decide(app, it, intent.action == APPROVE) }
            PAIR_CODE -> {
                val code = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(Pairing.KEY_CODE)?.toString().orEmpty()
                val port = intent.getIntExtra("port", -1)
                if (code.isNotBlank()) Pairing.submitCode(app, port, code)
            }
        }
    }
    companion object {
        const val PAUSE = "com.qcenaman.mobiletokensaver.PAUSE"
        const val KILL = "com.qcenaman.mobiletokensaver.KILL"
        const val PAIR_CODE = "com.qcenaman.mobiletokensaver.PAIR_CODE"
        const val APPROVE = "com.qcenaman.mobiletokensaver.APPROVE"
        const val DENY = "com.qcenaman.mobiletokensaver.DENY"
    }
}
