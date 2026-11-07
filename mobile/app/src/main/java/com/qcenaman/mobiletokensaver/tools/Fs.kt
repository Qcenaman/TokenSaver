package com.qcenaman.mobiletokensaver.tools

import android.os.Environment
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.*
import java.io.File

/** Phone-storage tools: find / read / write / scan. Paths default to shared storage. */
object Fs {
    private val shared: String get() = Environment.getExternalStorageDirectory().path

    private fun resolve(p: String): File {
        var s = p.trim()
        if (s.startsWith("~")) s = shared + s.drop(1)
        if (!s.startsWith("/")) s = "$shared/$s"
        return File(s)
    }

    val find = Tool("find", "Find files by name/text under one or more paths.",
        """{"type":"object","properties":{"path":{"type":"array","items":{"type":"string"}},"name":{"type":"string"},"text":{"type":"string"},"limit":{"type":"integer"},"recursive":{"type":"boolean"}},"required":["path"]}""") { a ->
        val roots = a.list("path").map { it.toString() }.ifEmpty { listOf(shared) }
        val name = a.string("name"); val text = a.string("text")
        val limit = a.int("limit", 200); val rec = a.bool("recursive", true)
        val hits = ArrayList<String>()
        for (r in roots) walk(resolve(r), rec) { f ->
            if (hits.size >= limit) return@walk
            if (name.isNotEmpty() && !f.name.contains(name.trim('*'), true)) return@walk
            if (text.isNotEmpty()) {
                val m = runCatching { f.length() < 2_000_000 && f.readText().contains(text, true) }.getOrDefault(false)
                if (!m) return@walk
            }
            hits.add("${f.path}  ${f.length()}")
        }
        ToolResult.ok(if (hits.isEmpty()) "no matches" else hits.joinToString("\n"))
    }

    val read = Tool("read", "Read part of a file: mode head/tail/lines/grep/info/text.",
        """{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string"},"from_line":{"type":"integer"},"lines":{"type":"integer"},"pattern":{"type":"string"},"max_chars":{"type":"integer"}},"required":["path"]}""") { a ->
        val f = resolve(a.string("path"))
        val mode = a.string("mode", "head").lowercase()
        if (mode == "info") {
            if (f.exists()) return@Tool ToolResult.ok("${f.path}\nsize=${f.length()} dir=${f.isDirectory} modified=${f.lastModified()}")
            val (c, o) = Shell.run(App.ctx, "stat ${ApkInstall.q(f.path)} 2>&1")
            return@Tool ToolResult(o.trim(), isError = c != 0)
        }
        val text = (if (f.canRead() && f.isFile) runCatching { f.readText() }.getOrNull() else null)
            ?: Shell.run(App.ctx, "[ -f ${ApkInstall.q(f.path)} ] && head -c 8000000 ${ApkInstall.q(f.path)}").let { (c, o) -> if (c == 0) o else null }
            ?: return@Tool ToolResult.err("not found or unreadable: ${f.path}")
        val lines by lazy { text.lines() }
        when (mode) {
            "tail" -> ToolResult.ok(lines.takeLast(a.int("lines", 40)).joinToString("\n"))
            "lines" -> { val from = a.int("from_line", 1); ToolResult.ok(lines.drop(from - 1).take(a.int("lines", 40)).joinToString("\n")) }
            "grep" -> { val p = a.string("pattern"); ToolResult.ok(lines.filter { it.contains(p, true) }.take(a.int("lines", 60)).joinToString("\n")) }
            "text" -> ToolResult.ok(text.take(a.int("max_chars", 8000)))
            else -> ToolResult.ok(lines.take(a.int("lines", 40)).joinToString("\n"))
        }
    }

    val write = Tool("write", "Create or change files and folders. op write/append/edit (find+replace) on path; move/copy to `to`; delete (folders recursively); mkdir. Falls back to the shell for paths the app itself cannot touch.",
        """{"type":"object","properties":{"path":{"type":"string"},"op":{"type":"string","enum":["write","append","edit","move","copy","delete","mkdir"]},"content":{"type":"string"},"find":{"type":"string"},"replace":{"type":"string"},"to":{"type":"string"}},"required":["path"]}""") { a ->
        val f = resolve(a.string("path"))
        val op = a.string("op", "write").lowercase()
        val to = a.string("to").let { if (it.isEmpty()) null else resolve(it) }
        if (op in setOf("move", "copy") && to == null) return@Tool ToolResult.err("`to` required for $op")
        try {
            when (op) {
                "append" -> { f.parentFile?.mkdirs(); f.appendText(a.string("content")); ToolResult.ok("appended ${a.string("content").length} chars") }
                "edit" -> {
                    val s = f.readText(); val find = a.string("find")
                    if (find.isEmpty() || !s.contains(find)) return@Tool ToolResult.err("find text not present")
                    f.writeText(s.replace(find, a.string("replace"))); ToolResult.ok("edited")
                }
                "move", "copy", "delete", "mkdir" -> fileOp(op, f, if (op == "move" || op == "copy") to!! else f)
                else -> { f.parentFile?.mkdirs(); f.writeText(a.string("content")); ToolResult.ok("wrote ${f.length()} bytes to ${f.path}") }
            }
        } catch (e: Exception) {
            if (op in setOf("write", "append")) shellWrite(f, a.string("content"), op == "append") else ToolResult.err("${e.javaClass.simpleName}: ${e.message}")
        }
    }

    /** Java first; when the app lacks access (Android/data, /data/local/tmp …), the same thing through the shell. */
    private fun fileOp(op: String, f: File, to: File): ToolResult {
        if (op != "mkdir" && !f.exists()) {
            val (c, _) = Shell.run(App.ctx, "[ -e ${ApkInstall.q(f.path)} ]")
            if (c != 0) return ToolResult.err("not found: ${f.path}")
        }
        val dest = if (op != "delete" && op != "mkdir" && to.isDirectory) File(to, f.name) else to
        val done = runCatching {
            when (op) {
                "mkdir" -> f.isDirectory || f.mkdirs()
                "delete" -> f.deleteRecursively() && !f.exists()
                "move" -> { dest.parentFile?.mkdirs(); f.renameTo(dest) || (f.copyRecursively(dest, overwrite = true) && f.deleteRecursively()) }
                else -> { dest.parentFile?.mkdirs(); f.copyRecursively(dest, overwrite = true) }
            }
        }.getOrDefault(false)
        if (done) return ToolResult.ok(msg(op, f, dest))
        val q = ApkInstall::q
        val cmd = when (op) {
            "mkdir" -> "mkdir -p ${q(f.path)}"
            "delete" -> "rm -rf ${q(f.path)}"
            "move" -> "mkdir -p ${q(dest.parent ?: "/")} && mv -f ${q(f.path)} ${q(dest.path)}"
            else -> "mkdir -p ${q(dest.parent ?: "/")} && cp -rf ${q(f.path)} ${q(dest.path)}"
        }
        val (c, o) = Shell.run(App.ctx, "$cmd 2>&1")
        return if (c == 0) ToolResult.ok(msg(op, f, dest) + " (via shell)") else ToolResult.err(o.trim().ifEmpty { "exit $c" })
    }

    private fun msg(op: String, f: File, dest: File) = when (op) {
        "mkdir" -> "made ${f.path}"; "delete" -> "deleted ${f.path}"
        "move" -> "moved ${f.path} → ${dest.path}"; else -> "copied ${f.path} → ${dest.path}"
    }

    private fun shellWrite(f: File, content: String, append: Boolean): ToolResult {
        val b64 = android.util.Base64.encodeToString(content.toByteArray(), android.util.Base64.NO_WRAP)
        val q = ApkInstall.q(f.path)
        val (c, o) = Shell.run(App.ctx, "mkdir -p ${ApkInstall.q(f.parent ?: "/")} && echo $b64 | base64 -d ${if (append) ">>" else ">"} $q 2>&1")
        return if (c == 0) ToolResult.ok("${if (append) "appended to" else "wrote"} ${f.path} (via shell)") else ToolResult.err(o.trim().ifEmpty { "exit $c" })
    }

    val scan = Tool("scan", "List a folder's immediate entries with sizes (limit, default 300). Uses the shell for folders the app cannot list.",
        """{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}""") { a ->
        val d = resolve(a.string("path"))
        val lim = a.int("limit", 300)
        val kids = d.listFiles()?.sortedBy { it.name }
        if (kids == null) {
            val (c, o) = Shell.run(App.ctx, "ls -la ${ApkInstall.q(d.path)} 2>&1 | head -${lim + 1}")
            return@Tool if (c == 0) ToolResult.ok(o.trim()) else ToolResult.err("cannot list ${d.path}: ${o.trim()}")
        }
        if (kids.isEmpty()) return@Tool ToolResult.ok("(empty)")
        ToolResult.ok(kids.take(lim).joinToString("\n") { (if (it.isDirectory) "d " else "  ") + "${it.name}  ${it.length()}" } +
            if (kids.size > lim) "\n… ${kids.size - lim} more" else "")
    }

    private fun walk(f: File, rec: Boolean, cb: (File) -> Unit) {
        if (f.isDirectory) { f.listFiles()?.forEach { if (it.isDirectory) { if (rec) walk(it, true, cb) } else cb(it) } }
        else cb(f)
    }

    val all = listOf(find, read, write, scan)
}
