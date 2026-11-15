package com.qcenaman.mobiletokensaver.tools

import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.engine.Ocr
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.engine.UiAdbSetup
import com.qcenaman.mobiletokensaver.mcp.*
import com.qcenaman.mobiletokensaver.service.A11yService

/** ui — the default way to see and drive the screen, via the accessibility tree + shell input fallback. */
object Ui {
    val ui = Tool("ui", "Read the screen as a compact text tree, or act on it. action: tree/tap (label or x,y)/swipe/type (into the focused field)/key (back, home, recents, notifications, quick_settings, lock, screenshot, or a keycode)/scroll_to/ocr (screen text with bounds via on-device OCR)/adb_setup (switch on Wireless debugging in Settings and pair with the on-screen code; the watchdog also does this by itself while the screen is off)/set_pin (lock-screen PIN adb_setup may use to unlock; pin). Works without the shell through accessibility.",
        """{"type":"object","properties":{"action":{"type":"string"},"label":{"type":"string"},"text":{"type":"string"},"x":{"type":"integer"},"y":{"type":"integer"},"x2":{"type":"integer"},"y2":{"type":"integer"},"key":{"type":"string"},"max":{"type":"integer"},"pin":{"type":"string"}}}""") { a ->
        val svc = A11yService.instance
        when (a.string("action", "tree").lowercase()) {
            "tree" -> svc?.let { ToolResult.ok(it.treeText(a.int("max", 200)).ifEmpty { "(no readable elements)" }) }
                ?: ToolResult.err("accessibility service not enabled")
            "tap" -> {
                if (a["x"] != null) ok(svc?.tapAt(a.int("x", 0), a.int("y", 0)) == true || shellTap(a.int("x", 0), a.int("y", 0)))
                else ok(svc?.tapLabel(a.string("label")) == true)
            }
            "swipe" -> ok(svc?.swipe(a.int("x", 0), a.int("y", 0), a.int("x2", 0), a.int("y2", 0)) == true ||
                shell("input swipe ${a.int("x",0)} ${a.int("y",0)} ${a.int("x2",0)} ${a.int("y2",0)} 250"))
            "type" -> ok(svc?.typeText(a.string("text")) == true || shell("input text ${ApkInstall.q(a.string("text").replace(" ", "%s"))}"))
            "key" -> ok(svc?.globalKey(a.string("key")) ?: shell("input keyevent ${ApkInstall.q(a.string("key"))}"))
            "scroll_to" -> ok(svc?.scrollToText(a.string("text")) == true)
            "ocr" -> svc?.let { s -> ToolResult.ok(Ocr.lines(s).joinToString("\n") { "${it.text} [${it.bounds.left},${it.bounds.top},${it.bounds.right},${it.bounds.bottom}]" }.ifEmpty { "(no text found)" }) }
                ?: ToolResult.err("accessibility service not enabled")
            "adb_setup" -> UiAdbSetup.run(App.ctx).let { if (it.ok) ToolResult.ok(it.msg) else ToolResult.err(it.msg) }
            "set_pin" -> { Prefs.unlockPin = a.string("pin").filter { it.isDigit() }; ToolResult.ok(if (Prefs.unlockPin.isEmpty()) "PIN cleared" else "PIN saved") }
            else -> ToolResult.err("unknown action")
        }
    }

    private fun ok(b: Boolean) = if (b) ToolResult.ok("ok") else ToolResult.err("failed")
    private fun shell(cmd: String): Boolean = Shell.run(App.ctx, cmd).first == 0
    private fun shellTap(x: Int, y: Int) = shell("input tap $x $y")

    val all = listOf(ui)
}
