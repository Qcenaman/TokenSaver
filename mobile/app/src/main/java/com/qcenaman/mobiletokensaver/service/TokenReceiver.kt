package com.qcenaman.mobiletokensaver.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.qcenaman.mobiletokensaver.Prefs

/**
 * `adb shell am broadcast -n com.qcenaman.mobiletokensaver/.service.TokenReceiver`
 * Hands the app's access token to adb as the broadcast's result data, so the PC's `phone pair` needs no copying
 * by hand on release builds (where `run-as` is refused). Only a sender holding DUMP can reach this receiver: the
 * adb shell, which already outranks the token. Only that sender sees the result. Off while "Link to my PC" is off.
 */
class TokenReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (!isOrderedBroadcast) return
        if (Prefs.pcLinkOn) { resultCode = 1; resultData = Prefs.token } else { resultCode = 0; resultData = "Link to my PC is off" }
    }
}
