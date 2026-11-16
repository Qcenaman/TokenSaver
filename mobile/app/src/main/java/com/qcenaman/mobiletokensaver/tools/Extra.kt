package com.qcenaman.mobiletokensaver.tools

import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.*
import java.io.File

/** saved: named shell commands kept on the phone; pc: the user's desktop PC through its TokenSaver; peer: every device in the shared registry. */
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

    val pc = Tool("pc", "The user's desktop PC through its TokenSaver (all its tools: scan find read run run_job job system web window browser media office rag local_ai saved check). Pass tool + args to call one; reached over LAN, else Tailscale, else a public address or HTTPS tunnel (no Tailscale needed), else relayed through another of the user's devices, else woken and retried. action status (probe every route) / wake (Wake-on-LAN from here and the devices at home, then wait until it answers) / route (set a route by hand: kind public|tunnel + value, \"\" clears).",
        """{"type":"object","properties":{"action":{"type":"string","enum":["call","status","wake","route"]},"tool":{"type":"string"},"args":{"type":"object"},"kind":{"type":"string","enum":["public","tunnel"]},"value":{"type":"string"}}}""") { a ->
        if (!com.qcenaman.mobiletokensaver.Prefs.pcLinkOn) return@Tool ToolResult.err("Link to my PC is off in MobileTokenSaver (turn it on in the app)")
        when (a.string("action", "call").lowercase()) {
            "status" -> {
                val p = com.qcenaman.mobiletokensaver.net.PcLink.pc() ?: return@Tool ToolResult.err("no PC link yet (run `phone pair` on the PC while the phone is on the LAN)")
                var probes = com.qcenaman.mobiletokensaver.net.PcLink.probeAll()
                if (!probes.contains(" up ")) { // all down: one more look after a pause (a waking PC or Tailscale path needs a moment)
                    try { Thread.sleep(5_000) } catch (_: InterruptedException) {}
                    probes = com.qcenaman.mobiletokensaver.net.PcLink.probeAll() + "\n(second look after 5 s)"
                    if (!probes.contains(" up ")) probes += "\nall direct routes down: pc calls now go through the other devices (relay) and wake the PC; pc wake does it now"
                }
                val out = ToolResult.ok("pc=${p.name} port=${p.port} ${p.summary()}\n$probes")
                com.qcenaman.mobiletokensaver.net.PcLink.reportSoon(App.ctx, "pc status")
                out
            }
            "wake" -> ToolResult.ok(com.qcenaman.mobiletokensaver.net.PcLink.wakeAndWait())
            "route" -> ToolResult.ok(com.qcenaman.mobiletokensaver.net.PcLink.setManualRoute(a.string("kind"), a.string("value")))
            else -> {
                val tool = a.string("tool"); if (tool.isEmpty()) return@Tool ToolResult.err("tool required")
                com.qcenaman.mobiletokensaver.net.PcLink.call(tool, com.qcenaman.mobiletokensaver.mcp.Json.encode(a["args"] ?: emptyMap<String, Any?>()))
            }
        }
    }

    val peer = Tool("peer", "All the user's devices running TokenSaver or MobileTokenSaver (desktop, laptop, Michie Pc, phone, tablet, TV) from one shared registry that syncs itself: no tokens to handle, never shown. call: run one of a device's tools (peer = its name or IP, tool + args); reached over LAN, else Tailscale, else its tunnel, else relayed through another device, else woken with Wake-on-LAN and retried for ~90 s. status: every device and which routes answer. wake: wake one and wait. relay: call through a chosen device (via). sync: swap registries with every device now (done every 10 min and when this device's address changes).",
        """{"type":"object","properties":{"action":{"type":"string","enum":["call","status","wake","relay","sync"]},"peer":{"type":"string"},"via":{"type":"string"},"tool":{"type":"string"},"args":{"type":"object"},"hops":{"type":"integer","description":"internal: set by a relaying device"}},"required":["action"]}""") { a ->
        val P = com.qcenaman.mobiletokensaver.net.Peers
        val hops = a.int("hops", 0)
        val args = com.qcenaman.mobiletokensaver.mcp.Json.encode(a["args"] ?: emptyMap<String, Any?>())
        when (a.string("action", "status").lowercase()) {
            "status" -> ToolResult.ok(P.status())
            "sync" -> ToolResult.ok(P.syncAll())
            "wake" -> {
                val (n, e) = P.lookup(a.string("peer")) ?: return@Tool ToolResult.err("${a.string("peer")} is not in the registry")
                val sent = P.wake(n, e, hops)
                if (hops > 0) return@Tool ToolResult.ok(sent)
                for (d in longArrayOf(5_000, 10_000, 15_000, 20_000, 20_000, 20_000)) {
                    try { Thread.sleep(d) } catch (_: InterruptedException) { break }
                    P.reach(e, n).first?.let { return@Tool ToolResult.ok("$sent\n$n is up (${it.second})") }
                }
                ToolResult.ok("$sent\n$n not answering yet after ~90 s")
            }
            "relay" -> {
                val peer = a.string("peer"); val via = a.string("via"); val tool = a.string("tool")
                if (peer.isEmpty() || via.isEmpty() || tool.isEmpty()) return@Tool ToolResult.err("relay needs peer, via and tool")
                P.viaRelay(via, peer, tool, args)
            }
            "call" -> {
                val peer = a.string("peer"); val tool = a.string("tool")
                if (peer.isEmpty() || tool.isEmpty()) return@Tool ToolResult.err("call needs peer and tool")
                if (tool == "peer" && hops == 0) return@Tool ToolResult.err("call a tool on the peer itself, not its peer tool (relay does that)")
                P.call(peer, tool, args, hops)
            }
            else -> ToolResult.err("unknown action")
        }
    }

    val all = listOf(saved, pc, peer)
}
