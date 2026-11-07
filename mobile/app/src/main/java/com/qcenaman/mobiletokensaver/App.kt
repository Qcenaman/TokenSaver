package com.qcenaman.mobiletokensaver

import android.annotation.SuppressLint
import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import org.conscrypt.Conscrypt
import java.security.Security

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        ctx = applicationContext
        Security.insertProviderAt(Conscrypt.newProvider(), 1)
        Prefs.init(this)
        CmdLog.init(this)
        Log.init(this)
        com.qcenaman.mobiletokensaver.auth.Auth.init(this)
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CH_ENGINE, "Engine", NotificationManager.IMPORTANCE_LOW).apply { setShowBadge(false) })
        nm.createNotificationChannel(NotificationChannel(CH_PAIR, "Sign-in approvals and pairing", NotificationManager.IMPORTANCE_HIGH))
        nm.createNotificationChannel(NotificationChannel(CH_ALERT, "Alerts", NotificationManager.IMPORTANCE_DEFAULT))
    }

    companion object {
        @SuppressLint("StaticFieldLeak")
        lateinit var ctx: Context
            private set
        const val CH_ENGINE = "engine"
        const val CH_PAIR = "pair"
        const val CH_ALERT = "alert"
    }
}
