package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import android.content.pm.PackageManager
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.adb.AdbManager
import rikka.shizuku.Shizuku
import java.io.File

/** Ways to run one shell line with shell (or root) power, used only to start the helper. */
object Privileged {
    interface Launcher {
        val name: String
        fun usable(ctx: Context): Boolean
        fun launch(ctx: Context, cmd: String): Boolean
    }

    @Volatile var via: String = ""

    object ShizukuL : Launcher {
        override val name = "shizuku"
        override fun usable(ctx: Context) = shizukuState() == ShizukuState.READY
        override fun launch(ctx: Context, cmd: String): Boolean {
            // Shizuku 13 keeps newProcess private; it is still the simplest way to run one line as its uid.
            val m = Shizuku::class.java.getDeclaredMethod("newProcess", Array<String>::class.java, Array<String>::class.java, String::class.java)
            m.isAccessible = true
            val p = m.invoke(null, arrayOf("sh", "-c", cmd), null, null) as Process
            p.inputStream.readBytes(); p.waitFor()
            return true
        }
    }

    object AdbL : Launcher {
        override val name = "adb"
        // Wireless debugging needs Wi-Fi; once adbd is in TCP mode it also answers on loopback without it.
        override fun usable(ctx: Context) = Keeper.wifiUp(ctx) || AdbManager.loopbackOpen()
        override fun launch(ctx: Context, cmd: String): Boolean {
            val adb = AdbManager.get(ctx)
            if (!adb.ensureConnected(ctx)) return false
            adb.capture("$cmd\nsleep 1; echo started")
            return true
        }
    }

    object RootL : Launcher {
        override val name = "root"
        override fun usable(ctx: Context) = suPath() != null
        override fun launch(ctx: Context, cmd: String): Boolean {
            val p = ProcessBuilder(suPath()!!, "-c", cmd).redirectErrorStream(true).start()
            p.inputStream.readBytes()
            return p.waitFor() == 0
        }
    }

    val launchers: List<Launcher> = listOf(ShizukuL, AdbL, RootL)

    fun suPath(): String? = listOf("/system/bin/su", "/system/xbin/su", "/sbin/su", "/debug_ramdisk/su").firstOrNull { File(it).exists() }

    enum class ShizukuState { NOT_RUNNING, OLD_VERSION, NO_PERMISSION, READY }

    fun shizukuState(): ShizukuState = try {
        when {
            !Shizuku.pingBinder() -> ShizukuState.NOT_RUNNING
            Shizuku.isPreV11() -> ShizukuState.OLD_VERSION
            Shizuku.checkSelfPermission() != PackageManager.PERMISSION_GRANTED -> ShizukuState.NO_PERMISSION
            else -> ShizukuState.READY
        }
    } catch (_: Throwable) { ShizukuState.NOT_RUNNING }

    fun requestShizuku(code: Int = 7) { runCatching { Shizuku.requestPermission(code) } }

    /** Once the helper runs, give the app the powers it would otherwise need adb for. */
    fun afterStart(ctx: Context) {
        val pkg = ctx.packageName
        Thread {
            Shell.run(ctx, listOf(
                "pm grant $pkg android.permission.WRITE_SECURE_SETTINGS",
                "pm grant $pkg android.permission.POST_NOTIFICATIONS",
                "appops set --uid $pkg MANAGE_EXTERNAL_STORAGE allow",
                "dumpsys deviceidle whitelist +$pkg",
                "cmd notification allow_listener $pkg/$pkg.service.NotifService",
            ).joinToString("; ") { "$it >/dev/null 2>&1" })
            CmdLog.add("self-grants applied (secure settings, all files, battery, notifications)")
        }.apply { isDaemon = true }.start()
    }
}
