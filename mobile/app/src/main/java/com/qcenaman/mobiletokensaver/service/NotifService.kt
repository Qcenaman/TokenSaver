package com.qcenaman.mobiletokensaver.service

import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification

/** Optional notification access: list/read notifications for the notifications tool. */
class NotifService : NotificationListenerService() {
    override fun onListenerConnected() { instance = this }
    override fun onListenerDisconnected() { if (instance === this) instance = null }

    fun dump(): String = try {
        (activeNotifications ?: emptyArray()).joinToString("\n") { sbn ->
            val e = sbn.notification.extras
            val title = e.getCharSequence("android.title")?.toString().orEmpty()
            val text = e.getCharSequence("android.text")?.toString().orEmpty()
            "${sbn.packageName}: $title — $text"
        }
    } catch (e: Exception) { "" }

    companion object { @Volatile var instance: NotifService? = null; private set }
}
