package com.qcenaman.mobiletokensaver.tools

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import com.qcenaman.mobiletokensaver.mcp.ToolResult
import com.qcenaman.mobiletokensaver.service.A11yService

/** Shell-free paths: open an app or one of its Settings pages from the app itself, and flip the page's switch via accessibility. */
object Pages {
    /** Page names → Settings actions that take package:<pkg>. Toggle pages have one main switch. */
    val actions = mapOf(
        "info" to Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
        "all_files" to Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION,
        "overlay" to Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
        "write_settings" to Settings.ACTION_MANAGE_WRITE_SETTINGS,
        "install_unknown" to Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
        "usage_access" to Settings.ACTION_USAGE_ACCESS_SETTINGS,
        "notifications" to Settings.ACTION_APP_NOTIFICATION_SETTINGS,
        "battery" to Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
    )
    val toggles = setOf("all_files", "overlay", "write_settings", "install_unknown")

    /** appops op → page whose switch sets it. */
    val opPage = mapOf(
        "MANAGE_EXTERNAL_STORAGE" to "all_files",
        "SYSTEM_ALERT_WINDOW" to "overlay",
        "WRITE_SETTINGS" to "write_settings",
        "REQUEST_INSTALL_PACKAGES" to "install_unknown",
    )

    fun intent(page: String, pkg: String): Intent? {
        val a = actions[page] ?: return null
        val i = Intent(a).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        when (page) {
            "notifications" -> i.putExtra(Settings.EXTRA_APP_PACKAGE, pkg)
            "usage_access" -> {} // list page only
            else -> i.data = Uri.parse("package:$pkg")
        }
        return i
    }

    fun open(ctx: Context, page: String, pkg: String): ToolResult {
        if (page == "app") {
            val i = ctx.packageManager.getLaunchIntentForPackage(pkg) ?: return ToolResult.err("$pkg has no launcher activity")
            ctx.startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            return ToolResult.ok("launched $pkg")
        }
        val i = intent(page, pkg) ?: return ToolResult.err("unknown page; use app, ${actions.keys.joinToString(", ")}")
        return try { ctx.startActivity(i); ToolResult.ok("opened $page for $pkg") } catch (e: Exception) { ToolResult.err("could not open $page: ${e.message}") }
    }

    /** Open [page] for [pkg] and set its main switch; presses Back afterwards. */
    fun toggle(ctx: Context, page: String, pkg: String, on: Boolean): ToolResult {
        val svc = A11yService.instance ?: return ToolResult.err("needs the accessibility service: turn on MobileTokenSaver in Settings → Accessibility")
        val opened = open(ctx, page, pkg)
        if (opened.isError) return opened
        Thread.sleep(800)
        val state = svc.setMainToggle(on) ?: return ToolResult.err("opened $page for $pkg but found no switch; use ui tree/tap")
        Thread.sleep(300)
        svc.globalKey("back")
        return if ((state == "on") == on) ToolResult.ok("$page for $pkg is now $state (via Settings)") else ToolResult.err("switch did not change; it is $state")
    }
}
