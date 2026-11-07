package com.qcenaman.mobiletokensaver.tools

import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.*
import java.io.File

/** saved: named shell commands kept on the phone; pc: the user's desktop PC through its TokenSaver. */
object Extra {
    private fun savedDir() = File(App.ctx.filesDir, "saved").also { it.mkdirs() }
    private fun safe(n: String) = n.replace(Regex("[^A-Za-z0-9._-]"), "_").take(80)

    val saved = Tool("saved", "Named saved shell commands: action list/show/save/remove/run (run returns the last `tail` lines, default 40).",
        """{"type":"object","properties":{"action":{"type":"string","enum":["list","show","save","remove","run"]},"name":{"type":"string"},"command":{"type":"string"},"tail":{"type":"integer"}}}""") { a ->
        val name = safe(a.string("name"))
        val f = File(savedDir(), name)
        when (a.string("action", "list").lowercase()) {
            "list" -> ToolResult.ok(savedDir().listFiles()?.joinToString("\n") { it.name }?.ifEmpty { null } ?: "(none)")
            "show" -> if (f.isFile) ToolResult.ok(f.readText()) else ToolResult.err("not found")
            "save" -> { if (name.isEmpty()) return@Tool ToolResult.err("name required"); f.writeText(a.string("command")); ToolResult.ok("saved $name") }
            "remove" -> { f.delete(); ToolResult.ok("removed $name") }
            "run" -> {
                if (!f.isFile) return@Tool ToolResult.err("not found")
                val (c, o) = Shell.run(App.ctx, f.readText())
                ToolResult("exit=$c\n" + o.lines().takeLast(a.int("tail", 40)).joinToString("\n"), isError = c != 0)
            }
            else -> ToolResult.err("unknown action")
        }
    }

    val pc = Tool("pc", "The user's desktop PC through its TokenSaver (all its tools: scan find read run run_job job system web window browser media office rag local_ai saved check). Pass tool + args to call one; reached over LAN, else Tailscale, else a public address or HTTPS tunnel (no Tailscale needed). action status (probe every route) / wake (Wake-on-LAN) / route (set a route by hand: kind public|tunnel + value, \"\" clears).",
        """{"type":"object","properties":{"action":{"type":"string","enum":["call","status","wake","route"]},"tool":{"type":"string"},"args":{"type":"object"},"kind":{"type":"string","enum":["public","tunnel"]},"value":{"type":"string"}}}""") { a ->
        if (!com.qcenaman.mobiletokensaver.Prefs.pcLinkOn) return@Tool ToolResult.err("Link to my PC is off in MobileTokenSaver (turn it on in the app)")
        when (a.string("action", "call").lowercase()) {
            "status" -> {
                val p = com.qcenaman.mobiletokensaver.net.PcLink.pc() ?: return@Tool ToolResult.err("no PC link yet (run `phone pair` on the PC while the phone is on the LAN)")
                val out = ToolResult.ok("pc=${p.name} port=${p.port} ${p.summary()}\n${com.qcenaman.mobiletokensaver.net.PcLink.probeAll()}")
                com.qcenaman.mobiletokensaver.net.PcLink.reportSoon(App.ctx, "pc status")
                out
            }
            "wake" -> ToolResult.ok(com.qcenaman.mobiletokensaver.net.PcLink.wake())
            "route" -> ToolResult.ok(com.qcenaman.mobiletokensaver.net.PcLink.setManualRoute(a.string("kind"), a.string("value")))
            else -> {
                val tool = a.string("tool"); if (tool.isEmpty()) return@Tool ToolResult.err("tool required")
                com.qcenaman.mobiletokensaver.net.PcLink.call(tool, com.qcenaman.mobiletokensaver.mcp.Json.encode(a["args"] ?: emptyMap<String, Any?>()))
            }
        }
    }

    val all = listOf(saved, pc)
}
