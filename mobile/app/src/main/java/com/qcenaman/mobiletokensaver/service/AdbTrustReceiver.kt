package com.qcenaman.mobiletokensaver.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs

/**
 * `adb shell am broadcast -n com.qcenaman.mobiletokensaver/.service.AdbTrustReceiver --es keys "<pubkey or fingerprint>|..." [--ez replace true]`
 * Sets which computers' adb keys AdbPrompt may approve by itself. Shell-only (DUMP permission).
 */
class AdbTrustReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        val given = intent?.getStringExtra("keys").orEmpty().split('|', '\n').mapNotNull { AdbPrompt.normalize(it) }.toSet()
        val replace = intent?.getBooleanExtra("replace", false) == true
        Prefs.adbTrusted = if (replace) given else Prefs.adbTrusted + given
        CmdLog.add("adb-trust: ${Prefs.adbTrusted.size} trusted computer key(s)")
        resultData = Prefs.adbTrusted.joinToString(",")
    }
}
