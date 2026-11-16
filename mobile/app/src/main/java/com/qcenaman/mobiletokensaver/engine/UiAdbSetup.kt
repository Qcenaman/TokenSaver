package com.qcenaman.mobiletokensaver.engine

import android.accessibilityservice.AccessibilityService
import android.app.KeyguardManager
import android.content.Context
import android.content.Intent
import android.os.PowerManager
import android.provider.Settings
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import com.qcenaman.mobiletokensaver.adb.AdbManager
import com.qcenaman.mobiletokensaver.service.A11yService

/**
 * Last-resort route for the watchdog: when Wireless debugging is off (or our key is not paired) and nothing
 * privileged can switch it on, drive Settings like a person would — accessibility tree first, on-device OCR when
 * the text is not in the tree — then pair our own ADB client with the pairing code read off the screen.
 *
 * QR pairing is not attempted: Settings' "Pair device with QR code" opens this phone's camera to scan a code shown
 * on another screen, so the phone cannot pair with itself that way. The pairing-code route covers it.
 *
 * Screen rule (OLED): the automatic run only starts while the screen is off, and turns it off again when done.
 */
object UiAdbSetup {
    data class Result(val ok: Boolean, val msg: String)

    @Volatile var running = false; private set
    @Volatile var lastResult = ""; private set
    @Volatile private var lastAuto = 0L
    private const val AUTO_EVERY_MS = 20 * 60_000L

    private fun log(s: String) { CmdLog.add("adb-ui: $s") }

    fun wirelessOn(ctx: Context) = Settings.Global.getInt(ctx.contentResolver, "adb_wifi_enabled", 0) == 1
    private fun devOptionsOn(ctx: Context) = Settings.Global.getInt(ctx.contentResolver, Settings.Global.DEVELOPMENT_SETTINGS_ENABLED, 0) == 1

    /** Watchdog entry: rate-limited, needs accessibility, and never takes over a screen the user is looking at. */
    fun autoRun(ctx: Context): Result? {
        if (running || System.currentTimeMillis() - lastAuto < AUTO_EVERY_MS) return null
        if (A11yService.instance == null) return null
        val pm = ctx.getSystemService(PowerManager::class.java)
        if (pm.isInteractive) return null // user is on the phone: try again on a later tick
        lastAuto = System.currentTimeMillis()
        return run(ctx)
    }

    @Synchronized
    fun run(ctx: Context): Result {
        val svc = A11yService.instance ?: return done(false, "accessibility service not enabled")
        if (!Keeper.wifiUp(ctx)) return done(false, "no Wi-Fi (Wireless debugging needs it)")
        running = true
        val pm = ctx.getSystemService(PowerManager::class.java)
        val wasOn = pm.isInteractive
        @Suppress("DEPRECATION")
        val wl = pm.newWakeLock(PowerManager.SCREEN_BRIGHT_WAKE_LOCK or PowerManager.ACQUIRE_CAUSES_WAKEUP, "mts:adbui")
        try {
            wl.acquire(3 * 60_000L)
            Thread.sleep(600)
            if (!unlock(ctx, svc)) return done(false, "screen is locked and no unlock PIN is saved (ui action set_pin)")
            if (!devOptionsOn(ctx) && !enableDevOptions(ctx, svc)) return done(false, "could not switch on Developer options")
            if (!openWirelessDebugging(ctx, svc)) return done(false, "could not open Wireless debugging")
            if (!wirelessOn(ctx) && !switchOn(ctx, svc)) return done(false, "could not switch on Wireless debugging")
            log("wireless debugging is on")
            // Already paired before? Then the switch was all that was missing.
            if (runCatching { AdbManager.get(ctx).ensureConnected(ctx) }.getOrDefault(false)) return done(true, "wireless debugging on, adb connected")
            log("QR pairing skipped (it scans with this phone's camera); using the pairing code")
            return if (pairWithCode(ctx, svc)) done(true, "paired with pairing code, adb connected") else done(false, "pairing with the code failed")
        } catch (e: Throwable) {
            return done(false, "error: ${e.javaClass.simpleName}: ${e.message}")
        } finally {
            runCatching {
                svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK); Thread.sleep(300)
                svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME); Thread.sleep(300)
                if (!wasOn) svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_LOCK_SCREEN) // screen back off
            }
            runCatching { if (wl.isHeld) wl.release() }
            running = false
        }
    }

    /**
     * Shizuku is running but MobileTokenSaver is not allowed: ask for it and press "Allow all the time" in Shizuku's
     * dialog through accessibility. Only while the screen is off (never over what the user is doing); the screen goes
     * back off afterwards.
     */
    fun acceptShizuku(ctx: Context): Boolean {
        val svc = A11yService.instance ?: return false
        val pm = ctx.getSystemService(PowerManager::class.java)
        if (running || pm.isInteractive) return false
        running = true
        @Suppress("DEPRECATION")
        val wl = pm.newWakeLock(PowerManager.SCREEN_BRIGHT_WAKE_LOCK or PowerManager.ACQUIRE_CAUSES_WAKEUP, "mts:shizuku")
        try {
            wl.acquire(60_000L)
            Thread.sleep(600)
            if (!unlock(ctx, svc)) { log("shizuku: screen locked and no unlock PIN saved"); return false }
            Privileged.requestShizuku()
            val hit = waitFor(svc, listOf("Allow all the time", "Allow"), 8_000) ?: run { log("shizuku: no permission dialog seen"); return false }
            tap(svc, hit, exactOnly = true)
            for (i in 1..10) { Thread.sleep(400); if (Privileged.shizukuState() == Privileged.ShizukuState.READY) { log("shizuku: allowed"); return true } }
            log("shizuku: still not allowed after tapping \"$hit\"")
            return false
        } catch (e: Throwable) {
            log("shizuku: ${e.javaClass.simpleName}: ${e.message}"); return false
        } finally {
            runCatching { svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_LOCK_SCREEN) }
            runCatching { if (wl.isHeld) wl.release() }
            running = false
        }
    }

    private fun done(ok: Boolean, msg: String): Result { lastResult = msg; log(msg); return Result(ok, msg) }

    // ---- screen helpers: tree first, OCR second ----

    private fun seen(svc: A11yService, text: String, ocr: Boolean = true): Boolean =
        svc.findNode(text) != null || (ocr && Ocr.find(svc, text) != null)

    /** Exact label first (so "Allow" never hits "Always allow …"), then contains, then OCR. */
    private fun tap(svc: A11yService, text: String, exactOnly: Boolean = false): Boolean {
        if (svc.elements().any { it.label.equals(text, true) } && svc.tapLabel(text)) return true
        if (!exactOnly && svc.clickFound(text)) return true
        val ls = Ocr.lines(svc)
        val l = ls.firstOrNull { it.text.trim().equals(text, true) } ?: (if (exactOnly) null else ls.firstOrNull { it.text.contains(text, true) }) ?: return false
        return svc.tapAt(l.bounds.centerX(), l.bounds.centerY())
    }

    /** Wait for any of [texts]; OCR every few polls since it costs a screenshot. */
    private fun waitFor(svc: A11yService, texts: List<String>, ms: Long): String? {
        val end = System.currentTimeMillis() + ms
        var n = 0
        while (System.currentTimeMillis() < end) {
            texts.firstOrNull { svc.findNode(it) != null }?.let { return it }
            if (n++ % 4 == 3) { val ls = Ocr.lines(svc); texts.firstOrNull { t -> ls.any { it.text.contains(t, true) } }?.let { return it } }
            Thread.sleep(400)
        }
        return null
    }

    private fun scrollFind(svc: A11yService, text: String): Boolean {
        if (svc.scrollToFind(text)) return true
        val w = svc.resources.displayMetrics.widthPixels; val h = svc.resources.displayMetrics.heightPixels
        repeat(10) {
            if (Ocr.find(svc, text) != null) return true
            svc.slowDrag(w / 2f, h * 0.75f, w / 2f, h * 0.30f); Thread.sleep(500)
        }
        return Ocr.find(svc, text) != null
    }

    private fun open(svc: A11yService, action: String) {
        svc.startActivity(Intent(action).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK))
        Thread.sleep(1200)
    }

    // ---- steps ----

    private fun unlock(ctx: Context, svc: A11yService): Boolean {
        val km = ctx.getSystemService(KeyguardManager::class.java)
        if (!km.isKeyguardLocked) return true
        val w = svc.resources.displayMetrics.widthPixels; val h = svc.resources.displayMetrics.heightPixels
        svc.swipe(w / 2, (h * 0.85).toInt(), w / 2, (h * 0.30).toInt(), 300); Thread.sleep(800)
        if (!km.isKeyguardLocked) return true
        val pin = Prefs.unlockPin
        if (pin.isEmpty()) return false
        log("unlocking")
        enterPin(svc, pin, keypad = true)
        for (i in 1..10) { if (!km.isKeyguardLocked) return true; Thread.sleep(300) }
        return !km.isKeyguardLocked
    }

    /** Keypad keys read "6" or "6 MNO" (Samsung adds the letters). */
    private fun tapDigit(svc: A11yService, d: Char): Boolean {
        val e = svc.elements().firstOrNull { it.label == d.toString() } ?: svc.elements().firstOrNull { it.label.startsWith("$d ") }
            ?: return tap(svc, d.toString(), exactOnly = true)
        var n: android.view.accessibility.AccessibilityNodeInfo? = e.node
        while (n != null && !n.isClickable) n = n.parent
        return n?.performAction(android.view.accessibility.AccessibilityNodeInfo.ACTION_CLICK) ?: svc.tapAt(e.bounds.centerX(), e.bounds.centerY())
    }

    /** Lock screen: tap the keypad digits. Settings' confirm-lock page: type into the field (keypad as fallback). */
    private fun enterPin(svc: A11yService, pin: String, keypad: Boolean = false) {
        val typed = !keypad && svc.typeText(pin, append = false)
        if (!typed) for (d in pin) { if (!tapDigit(svc, d)) { if (keypad) svc.typeText(pin, append = false); break }; Thread.sleep(120) }
        Thread.sleep(300)
        listOf("OK", "Enter", "Done", "Next", "Continue").firstOrNull { l -> svc.elements().any { it.label.equals(l, true) } }?.let { tap(svc, it, exactOnly = true) }
        Thread.sleep(600)
    }

    private fun enableDevOptions(ctx: Context, svc: A11yService): Boolean {
        log("switching on Developer options (Build number ×7)")
        open(svc, Settings.ACTION_DEVICE_INFO_SETTINGS)
        if (!seen(svc, "Build number") && scrollFind(svc, "Software information")) { tap(svc, "Software information"); Thread.sleep(1000) }
        if (!scrollFind(svc, "Build number")) return false
        repeat(8) { tap(svc, "Build number"); Thread.sleep(250) }
        // Android asks for the screen lock before turning them on.
        if (!devOptionsOn(ctx) && waitFor(svc, listOf("PIN", "password", "Confirm"), 3000) != null && Prefs.unlockPin.isNotEmpty()) enterPin(svc, Prefs.unlockPin)
        Thread.sleep(800)
        return devOptionsOn(ctx)
    }

    private fun openWirelessDebugging(ctx: Context, svc: A11yService): Boolean {
        open(svc, Settings.ACTION_APPLICATION_DEVELOPMENT_SETTINGS)
        if (!scrollFind(svc, "Wireless debugging")) return false
        tap(svc, "Wireless debugging"); Thread.sleep(1200)
        // On the sub-page the row title is gone or becomes the page title; either way the switch/pair rows show up.
        return waitFor(svc, listOf("Pair device", "Use wireless debugging", "IP address"), 5000) != null || seen(svc, "Wireless debugging")
    }

    private fun switchOn(ctx: Context, svc: A11yService): Boolean {
        if (svc.setMainToggle(true, 3000) == null) listOf("Use wireless debugging", "Off").firstOrNull { tap(svc, it) }
        // "Allow wireless debugging on this network?"
        if (waitFor(svc, listOf("Allow"), 5000) != null) {
            if (seen(svc, "Always allow", ocr = false)) tap(svc, "Always allow")
            tap(svc, "Allow", exactOnly = true)
        }
        for (i in 1..15) { if (wirelessOn(ctx)) return true; Thread.sleep(400) }
        return wirelessOn(ctx)
    }

    private fun pairWithCode(ctx: Context, svc: A11yService): Boolean {
        if (!seen(svc, "Pair device with pairing code") && !scrollFind(svc, "pairing code")) return false
        if (!tap(svc, "Pair device with pairing code") && !tap(svc, "pairing code")) return false
        Thread.sleep(1500)
        repeat(4) { attempt ->
            val tree = svc.elements().map { it.label }
            var f = PairText.parse(tree)
            if (f.code == null || f.port <= 0) {
                val o = PairText.parse(Ocr.lines(svc).map { it.text })
                f = PairText.Found(f.code ?: o.code, f.host ?: o.host, if (f.port > 0) f.port else o.port)
            }
            if (f.code != null) {
                log("read pairing code (port ${if (f.port > 0) f.port else "via mDNS"}), attempt ${attempt + 1}")
                // adbd answers on every interface, so loopback works even when the dialog shows the Wi-Fi IP.
                if (AutoPair.pairWithCode(ctx, f.code, if (f.port > 0) "127.0.0.1" else null, f.port)) return true
            }
            Thread.sleep(1200)
        }
        return false
    }
}
