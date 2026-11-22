package com.qcenaman.mobiletokensaver.service

import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import com.qcenaman.mobiletokensaver.CmdLog
import com.qcenaman.mobiletokensaver.Prefs
import java.security.MessageDigest
import java.util.Base64

/**
 * Answers Android's "Allow USB debugging?" prompt by itself, but only for the user's own computers:
 * the RSA fingerprint shown in the dialog must be on the trusted list (Prefs.adbTrusted, set by
 * AdbTrustReceiver from the PC over adb). Ticks "Always allow" and taps Allow. Unknown keys are left alone.
 */
object AdbPrompt {
    private val FP = Regex("([0-9A-F]{2}:){15}[0-9A-F]{2}")
    @Volatile private var busy = false

    /** Android's fingerprint of an adb public key ("QAAAA... user@host"): MD5 of the decoded key, AA:BB:... */
    fun fingerprint(pubKey: String): String {
        val b64 = pubKey.trim().split(Regex("\\s+"))[0]
        val d = MessageDigest.getInstance("MD5").digest(Base64.getMimeDecoder().decode(b64))
        return d.joinToString(":") { "%02X".format(it) }
    }

    /** A string is either a fingerprint already or a public key; returns the fingerprint or null. */
    fun normalize(s: String): String? {
        val t = s.trim()
        if (t.isEmpty()) return null
        FP.matchEntire(t.uppercase())?.let { return it.value }
        return try { fingerprint(t) } catch (e: Exception) { null }
    }

    fun findFingerprint(texts: Iterable<String>): String? =
        texts.firstNotNullOfOrNull { FP.find(it.uppercase())?.value }

    fun isAdbPrompt(pkg: CharSequence?, cls: CharSequence?): Boolean =
        pkg?.toString() == "com.android.systemui" &&
            (cls?.contains("UsbDebugging") == true || cls?.contains("AdbDebugging") == true)

    fun shouldApprove(fp: String?, trusted: Set<String>): Boolean =
        fp != null && trusted.any { it.equals(fp, ignoreCase = true) }

    fun onEvent(svc: A11yService, e: AccessibilityEvent) {
        if (e.eventType != AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED) return
        if (!isAdbPrompt(e.packageName, e.className) || busy) return
        busy = true
        Thread {
            try { answer(svc) } catch (x: Exception) { CmdLog.add("adb-prompt: ${x.message}") } finally { busy = false }
        }.start()
    }

    private fun answer(svc: A11yService) {
        var fp: String? = null
        repeat(10) { if (fp == null) { Thread.sleep(300); fp = findFingerprint(svc.elements().map { it.label }) } }
        val trusted = Prefs.adbTrusted
        if (!shouldApprove(fp, trusted)) {
            CmdLog.add("adb-prompt: left for the user (key ${fp ?: "?"} not on the trusted list)")
            return
        }
        val roots = svc.windows?.mapNotNull { it.root } ?: listOfNotNull(svc.rootInActiveWindow)
        for (r in roots) {
            val box = r.findAccessibilityNodeInfosByViewId("com.android.systemui:id/alwaysUse")?.firstOrNull()
                ?: findCheckable(r)
            if (box != null && !box.isChecked) box.performAction(AccessibilityNodeInfo.ACTION_CLICK)
            val ok = r.findAccessibilityNodeInfosByViewId("android:id/button1")?.firstOrNull()
            if (ok != null) {
                Thread.sleep(300)
                val done = ok.performAction(AccessibilityNodeInfo.ACTION_CLICK)
                CmdLog.add("adb-prompt: allowed $fp (always) -> $done")
                return
            }
        }
        if (svc.tapLabel("Allow") || svc.tapLabel("OK")) CmdLog.add("adb-prompt: allowed $fp by label")
        else CmdLog.add("adb-prompt: trusted key $fp but no Allow button found")
    }

    private fun findCheckable(n: AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        if (n == null) return null
        if (n.isCheckable && n.isVisibleToUser) return n
        for (i in 0 until n.childCount) findCheckable(n.getChild(i))?.let { return it }
        return null
    }
}
