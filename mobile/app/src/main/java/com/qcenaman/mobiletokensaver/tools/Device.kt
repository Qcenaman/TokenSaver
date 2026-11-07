package com.qcenaman.mobiletokensaver.tools

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.Base64
import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.*
import java.io.ByteArrayOutputStream

/** screen / apps / notifications / system / web — device-level tools. */
object Device {
    private val ctx: Context get() = App.ctx

    val screen = Tool("screen", "Screenshot as a downscaled JPEG (only when asked). Optional max_px.",
        """{"type":"object","properties":{"max_px":{"type":"integer"}}}""") { a ->
        val png = Shell.runBytes(ctx, "screencap -p 2>/dev/null")
        if (png.isEmpty()) return@Tool ToolResult.err("screencap failed (shell channel down)")
        val bmp = BitmapFactory.decodeByteArray(png, 0, png.size) ?: return@Tool ToolResult.err("decode failed")
        val max = a.int("max_px", 900)
        val scale = minOf(1f, max.toFloat() / maxOf(bmp.width, bmp.height))
        val out = if (scale < 1f) Bitmap.createScaledBitmap(bmp, (bmp.width * scale).toInt(), (bmp.height * scale).toInt(), true) else bmp
        val bos = ByteArrayOutputStream(); out.compress(Bitmap.CompressFormat.JPEG, 70, bos)
        ToolResult.image(Base64.encodeToString(bos.toByteArray(), Base64.NO_WRAP), caption = "${out.width}x${out.height}")
    }

    val apps = Tool("apps", "Apps: list (filter, all=true adds system apps, limit) / info / launch / stop / install (url or path, sha256, size, test, replace) / uninstall / grant + revoke (permission, e.g. CAMERA or android.permission.CAMERA) / appops (op e.g. MANAGE_EXTERNAL_STORAGE, mode allow|deny|ignore|default|foreground; no mode = show; uid default true) / open (page) / toggle (page, on): open an app's Settings page or flip its switch through accessibility — works without the shell. appops and launch fall back to that automatically when the shell is down.",
        """{"type":"object","properties":{"action":{"type":"string","enum":["list","info","launch","stop","install","uninstall","grant","revoke","appops","open","toggle"]},"page":{"type":"string","description":"open/toggle: app, info, all_files, overlay, write_settings, install_unknown, usage_access, notifications, battery"},"on":{"type":"boolean","description":"toggle: switch on (default) or off"},"package":{"type":"string"},"filter":{"type":"string"},"all":{"type":"boolean"},"limit":{"type":"integer"},"permission":{"type":"string"},"op":{"type":"string"},"mode":{"type":"string"},"uid":{"type":"boolean"},"url":{"type":"string","description":"http(s) URL of the APK to download and install"},"path":{"type":"string","description":"APK path on the phone"},"sha256":{"type":"string","description":"expected hex sha256 of the APK"},"size":{"type":"integer","description":"expected size in bytes"},"test":{"type":"boolean","description":"allow test-only APKs (pm install -t)"},"replace":{"type":"boolean","description":"reinstall keeping data (pm install -r), default true"}}}""") { a ->
        val pkg = a.string("package").trim()
        val action = a.string("action", "list").lowercase()
        if (action !in setOf("list", "install") && pkg.isEmpty()) return@Tool ToolResult.err("package required")
        if (action == "open") return@Tool Pages.open(ctx, a.string("page", "app").lowercase(), pkg)
        if (action == "toggle") return@Tool Pages.toggle(ctx, a.string("page").lowercase(), pkg, a.bool("on", true))
        fun sh(cmd: String): ToolResult { val (c, o) = Shell.run(ctx, cmd); return ToolResult(o.trim().ifEmpty { if (c == 0) "ok" else "exit $c" }, isError = c != 0) }
        when (action) {
            "list" -> {
                val (_, o) = Shell.run(ctx, if (a.bool("all")) "pm list packages" else "pm list packages -3")
                val f = a.string("filter")
                val l = o.lines().map { it.removePrefix("package:").trim() }.filter { it.isNotEmpty() && (f.isEmpty() || it.contains(f, true)) }.sorted()
                val lim = a.int("limit", 300)
                ToolResult.ok((l.take(lim).joinToString("\n") + if (l.size > lim) "\n… ${l.size - lim} more" else "").ifEmpty { "no match" })
            }
            "info" -> sh("dumpsys package ${ApkInstall.q(pkg)} | grep -E 'versionName|versionCode|firstInstallTime|lastUpdateTime|targetSdk|granted=true' | head -${a.int("limit", 80)}")
            "launch" -> Pages.open(ctx, "app", pkg).let { if (!it.isError) it else sh(ApkInstall.launchCmd(pkg)).let { r -> if (r.isError) r else ToolResult.ok("launched $pkg") } }
            "stop" -> sh(ApkInstall.stopCmd(pkg)).let { if (it.isError) it else ToolResult.ok("stopped $pkg") }
            "install" -> install(a)
            "uninstall" -> sh(ApkInstall.uninstallCmd(pkg))
            "grant", "revoke" -> {
                val perm = a.string("permission").trim(); if (perm.isEmpty()) return@Tool ToolResult.err("permission required")
                sh(if (action == "grant") ApkInstall.grantCmd(pkg, perm) else ApkInstall.revokeCmd(pkg, perm)).let { if (it.isError) it else ToolResult.ok("$action ${ApkInstall.fullPermission(perm)} → $pkg") }
            }
            "appops" -> {
                val op = a.string("op").trim(); val mode = a.string("mode").trim().lowercase()
                if (mode.isNotEmpty() && op.isEmpty()) return@Tool ToolResult.err("op required with mode")
                if (mode.isNotEmpty() && mode !in ApkInstall.MODES) return@Tool ToolResult.err("mode must be one of ${ApkInstall.MODES}")
                val r = sh(ApkInstall.appopsCmd(pkg, op, mode, a.bool("uid", true)))
                val page = Pages.opPage[op.uppercase()]
                if (r.isError && page != null && (mode == "allow" || mode == "deny" || mode == "ignore" || mode == "default"))
                    return@Tool Pages.toggle(ctx, page, pkg, mode == "allow").let { t -> if (t.isError) ToolResult.err("shell: ${r.text}\nsettings: ${t.text}") else t }
                if (r.isError || mode.isEmpty()) r else sh(ApkInstall.appopsCmd(pkg, op, "", false)).let { ToolResult.ok("set $op $mode for $pkg\n${it.text}") }
            }
            else -> ToolResult.err("unknown action")
        }
    }

    /** Get the APK into the app's external cache (shell can read it), check it, stage into /data/local/tmp, pm install, clean up. */
    private fun install(a: Map<String, Any?>): ToolResult {
        val url = a.string("url").trim(); val path = a.string("path").trim()
        if (url.isEmpty() && path.isEmpty()) return ToolResult.err("install needs url or path")
        if (url.isNotEmpty() && !ApkInstall.isHttpUrl(url)) return ToolResult.err("url must be http or https")
        val dir = ctx.externalCacheDir ?: return ToolResult.err("external cache dir unavailable")
        dir.mkdirs()
        val token = java.util.UUID.randomUUID().toString().replace("-", "").take(12)
        val local = java.io.File(dir, "install-$token.apk")
        val staged = ApkInstall.stagedPath(token)
        try {
            if (url.isNotEmpty()) {
                download(url, local)?.let { return ToolResult.err(it) }
            } else {
                val (code, out) = Shell.run(ctx, "cat ${ApkInstall.q(path)} > ${ApkInstall.q(local.absolutePath)}")
                if (code != 0 || !local.exists()) return ToolResult.err("cannot read $path: ${out.trim().ifEmpty { "exit $code" }}")
            }
            val size = (a["size"] as? Number)?.toLong()
            ApkInstall.verify(local, size, a.string("sha256"))?.let { return ToolResult.err(it) }

            @Suppress("DEPRECATION")
            val info = ctx.packageManager.getPackageArchiveInfo(local.absolutePath, 0)
                ?: return ToolResult.err("not a valid APK (could not parse ${local.length()} bytes)")

            val (sc, so) = Shell.run(ctx, ApkInstall.stageCmd(local.absolutePath, staged))
            val stagedSize = so.trim().lines().lastOrNull()?.trim()?.toLongOrNull()
            if (sc != 0 || stagedSize != local.length())
                return ToolResult.err("staging to $staged failed: ${so.trim().ifEmpty { "exit $sc" }}")

            val (pc, po) = Shell.run(ctx, ApkInstall.installCmd(staged, a.bool("replace", true), a.bool("test", false)))
            ApkInstall.failure(po, pc)?.let { return ToolResult.err(it) }
            return ToolResult.ok("installed ${info.packageName} ${info.versionName ?: ""}".trim())
        } finally {
            runCatching { Shell.run(ctx, ApkInstall.cleanupCmd(staged)) }
            local.delete()
        }
    }

    /** Download in the engine (not via shell); null on success, else an error. */
    private fun download(url: String, dest: java.io.File): String? {
        try {
        var u = java.net.URL(url); var hops = 0
        while (true) {
            val c = u.openConnection() as java.net.HttpURLConnection
            c.instanceFollowRedirects = false
            c.setRequestProperty("User-Agent", "MobileTokenSaver"); c.connectTimeout = 15000; c.readTimeout = 60000
            val code = c.responseCode
            if (code in 300..399) {
                val loc = c.getHeaderField("Location") ?: return "redirect without Location"
                c.disconnect()
                u = java.net.URL(u, loc)
                if (u.protocol != "http" && u.protocol != "https") return "redirect to non-http url"
                if (++hops > 5) return "too many redirects"
                continue
            }
            if (code !in 200..299) { c.disconnect(); return "download failed: HTTP $code" }
            c.inputStream.use { inp -> dest.outputStream().use { inp.copyTo(it, 64 * 1024) } }
            c.disconnect()
            return null
        }
        } catch (e: Exception) { return "download failed: ${e.message}" }
    }

    val notifications = Tool("notifications", "List active notifications (via NotificationListener), newest last; limit (default 50), filter by package/text.",
        """{"type":"object","properties":{"limit":{"type":"integer"},"filter":{"type":"string"}}}""") { a ->
        val f = a.string("filter")
        com.qcenaman.mobiletokensaver.service.NotifService.instance?.let { s -> ToolResult.ok(s.dump().lines().filter { it.isNotBlank() && (f.isEmpty() || it.contains(f, true)) }.takeLast(a.int("limit", 50)).joinToString("\n").ifEmpty { "(none)" }) }
            ?: ToolResult.err("notification access not granted")
    }

    val system = Tool("system", "Phone state: battery, network, storage.",
        """{"type":"object","properties":{}}""") { _ ->
        val bat = Shell.run(ctx, "dumpsys battery | grep -E 'level|status|temperature'").second
        val net = Shell.run(ctx, "settings get global wifi_on; ip route 2>/dev/null | head -1").second
        val disk = Shell.run(ctx, "df -h /sdcard | tail -1").second
        ToolResult.ok("-- shell --\n${Shell.status(ctx)}\n-- addresses --\n${com.qcenaman.mobiletokensaver.net.Addresses.summary()}\n-- battery --\n$bat\n-- network --\n$net\n-- storage --\n$disk")
    }

    val web = Tool("web", "Fetch a URL on the phone and return clean text.",
        """{"type":"object","properties":{"url":{"type":"string"},"max_chars":{"type":"integer"}},"required":["url"]}""") { a ->
        val url = a.string("url")
        val html = runCatching {
            val c = (java.net.URL(url).openConnection() as java.net.HttpURLConnection)
            c.setRequestProperty("User-Agent", "Mozilla/5.0 MobileTokenSaver"); c.connectTimeout = 10000; c.readTimeout = 15000
            c.inputStream.bufferedReader().readText()
        }.getOrElse { return@Tool ToolResult.err("fetch failed: ${it.message}") }
        val text = html.replace(Regex("(?s)<script.*?</script>"), " ")
            .replace(Regex("(?s)<style.*?</style>"), " ")
            .replace(Regex("<[^>]+>"), " ").replace(Regex("[ \t]+"), " ")
            .replace(Regex("\n\\s*\n+"), "\n").trim()
        ToolResult.ok(text.take(a.int("max_chars", 6000)))
    }

    val all = listOf(screen, apps, notifications, system, web)
}
