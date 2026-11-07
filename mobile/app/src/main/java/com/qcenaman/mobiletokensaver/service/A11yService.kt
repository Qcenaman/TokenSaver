package com.qcenaman.mobiletokensaver.service

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.graphics.Path
import android.graphics.Rect
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** Reads the on-screen view tree as compact text and performs taps/gestures — the default "see the screen". */
class A11yService : AccessibilityService() {
    override fun onServiceConnected() { instance = this }
    override fun onDestroy() { if (instance === this) instance = null; super.onDestroy() }
    override fun onAccessibilityEvent(event: AccessibilityEvent?) {}
    override fun onInterrupt() {}

    data class El(val label: String, val bounds: Rect, val clickable: Boolean, val node: AccessibilityNodeInfo)

    /** Flat list of meaningful elements: "label [l,t,r,b]". */
    fun elements(): List<El> {
        val out = ArrayList<El>()
        val roots = windows?.mapNotNull { it.root } ?: listOfNotNull(rootInActiveWindow)
        for (r in roots) walk(r, out)
        return out
    }

    private fun walk(n: AccessibilityNodeInfo?, out: MutableList<El>) {
        if (n == null) return
        val label = (n.text ?: n.contentDescription ?: n.hintText)?.toString()?.trim().orEmpty()
        if (label.isNotEmpty() && n.isVisibleToUser) {
            val b = Rect(); n.getBoundsInScreen(b)
            out.add(El(label, b, n.isClickable, n))
        }
        for (i in 0 until n.childCount) walk(n.getChild(i), out)
    }

    fun treeText(max: Int = 200): String {
        val els = elements().take(max)
        return els.joinToString("\n") { e ->
            val b = e.bounds
            (if (e.clickable) "* " else "  ") + "${e.label} [${b.left},${b.top},${b.right},${b.bottom}]"
        }
    }

    fun tapLabel(label: String): Boolean {
        val e = elements().firstOrNull { it.label.equals(label, true) }
            ?: elements().firstOrNull { it.label.contains(label, true) } ?: return false
        var node: AccessibilityNodeInfo? = e.node
        while (node != null && !node.isClickable) node = node.parent
        return node?.performAction(AccessibilityNodeInfo.ACTION_CLICK) ?: tapAt(e.bounds.centerX(), e.bounds.centerY())
    }

    /** Put text into the focused input field (no shell needed). */
    fun typeText(text: String, append: Boolean = true): Boolean {
        val n = rootInActiveWindow?.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: findEditable(rootInActiveWindow) ?: return false
        val now = if (append) (n.text?.toString().orEmpty().takeUnless { n.isShowingHintText }.orEmpty() + text) else text
        val args = android.os.Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, now) }
        return n.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)
    }

    private fun findEditable(n: AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        if (n == null) return null
        if (n.isEditable && n.isVisibleToUser) return n
        for (i in 0 until n.childCount) findEditable(n.getChild(i))?.let { return it }
        return null
    }

    /** back, home, recents, notifications, quick_settings, power, lock, screenshot. */
    fun globalKey(key: String): Boolean? {
        val k = key.lowercase().removePrefix("keycode_")
        val action = when (k) {
            "back", "4" -> GLOBAL_ACTION_BACK
            "home", "3" -> GLOBAL_ACTION_HOME
            "recents", "app_switch", "187" -> GLOBAL_ACTION_RECENTS
            "notifications" -> GLOBAL_ACTION_NOTIFICATIONS
            "quick_settings" -> GLOBAL_ACTION_QUICK_SETTINGS
            "power", "power_dialog" -> GLOBAL_ACTION_POWER_DIALOG
            "lock", "lock_screen" -> GLOBAL_ACTION_LOCK_SCREEN
            "screenshot", "sysrq" -> GLOBAL_ACTION_TAKE_SCREENSHOT
            else -> return null
        }
        return performGlobalAction(action)
    }

    /**
     * On a settings page with one main switch (All files access, Display over other apps, Usage access …),
     * set it to [on]. Waits up to [waitMs] for the page. Returns "on"/"off" when done, null when no switch was found.
     */
    fun setMainToggle(on: Boolean, waitMs: Long = 6000): String? {
        val end = System.currentTimeMillis() + waitMs
        while (System.currentTimeMillis() < end) {
            val sw = findToggle(rootInActiveWindow)
            if (sw != null) {
                if (sw.isChecked != on) {
                    var c: AccessibilityNodeInfo? = sw
                    while (c != null && !c.isClickable) c = c.parent
                    val b = Rect(); sw.getBoundsInScreen(b)
                    if (c?.performAction(AccessibilityNodeInfo.ACTION_CLICK) != true) tapAt(b.centerX(), b.centerY())
                    Thread.sleep(700)
                    val again = findToggle(rootInActiveWindow)
                    return if (again?.isChecked ?: !sw.isChecked) "on" else "off"
                }
                return if (sw.isChecked) "on" else "off"
            }
            Thread.sleep(300)
        }
        return null
    }

    private fun findToggle(n: AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        if (n == null) return null
        val cls = n.className?.toString().orEmpty()
        if (n.isCheckable && n.isVisibleToUser && (cls.contains("Switch") || cls.contains("Toggle") || cls.contains("CheckBox"))) return n
        for (i in 0 until n.childCount) findToggle(n.getChild(i))?.let { return it }
        // Some Samsung pages expose only a checkable row
        return if (n.isCheckable && n.isVisibleToUser) n else null
    }

    fun tapAt(x: Int, y: Int): Boolean = gesture(x.toFloat(), y.toFloat(), x.toFloat(), y.toFloat(), 60)
    fun swipe(x1: Int, y1: Int, x2: Int, y2: Int, ms: Long = 250) =
        gesture(x1.toFloat(), y1.toFloat(), x2.toFloat(), y2.toFloat(), ms)

    private fun gesture(x1: Float, y1: Float, x2: Float, y2: Float, ms: Long): Boolean {
        val path = Path().apply { moveTo(x1, y1); lineTo(x2, y2) }
        val g = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path, 0, ms)).build()
        val latch = CountDownLatch(1); var ok = false
        dispatchGesture(g, object : GestureResultCallback() {
            override fun onCompleted(d: GestureDescription?) { ok = true; latch.countDown() }
            override fun onCancelled(d: GestureDescription?) { latch.countDown() }
        }, null)
        latch.await(ms + 2000, TimeUnit.MILLISECONDS)
        return ok
    }

    private fun visible(text: String) = elements().any { it.label.contains(text, true) }

    /** A node whose text/description contains [text], searched through the whole tree of the active window. */
    fun findNode(text: String): AccessibilityNodeInfo? {
        val root = rootInActiveWindow ?: return null
        return root.findAccessibilityNodeInfosByText(text)?.firstOrNull { it.isVisibleToUser }
            ?: elements().firstOrNull { it.label.contains(text, true) }?.node
    }

    /** Click the node found by [findNode] (or its nearest clickable parent). */
    fun clickFound(text: String): Boolean {
        var node = findNode(text) ?: return false
        val b = Rect(); node.getBoundsInScreen(b)
        while (!node.isClickable) node = node.parent ?: return tapAt(b.centerX(), b.centerY())
        return node.performAction(AccessibilityNodeInfo.ACTION_CLICK)
    }

    /**
     * Page through the first scrollable list with the accessibility scroll action (no finger gestures) until
     * [text] is in the tree; stops at the first hit, so it never runs past the row it is looking for.
     */
    fun scrollToFind(text: String, maxPages: Int = 12): Boolean {
        if (findNode(text) != null) return true
        val w = resources.displayMetrics.widthPixels; val h = resources.displayMetrics.heightPixels
        var useDrag = false
        repeat(maxPages) {
            val before = elements().map { it.label }
            val list = if (useDrag) null else scrollable(rootInActiveWindow)
            if (list == null || !list.performAction(AccessibilityNodeInfo.ACTION_SCROLL_FORWARD)) { useDrag = true; slowDrag(w / 2f, h * 0.75f, w / 2f, h * 0.30f) }
            Thread.sleep(600)
            if (findNode(text) != null) return true
            val after = elements().map { it.label }
            if (after == before) { if (useDrag) return false; useDrag = true } // the scroll action did nothing on this list: drag instead
        }
        return false
    }

    private fun scrollable(n: AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        if (n == null) return null
        if (n.isScrollable) return n
        for (i in 0 until n.childCount) scrollable(n.getChild(i))?.let { return it }
        return null
    }

    /** Scroll with slow drags that end at rest (no fling) until [text] is on screen. */
    fun scrollToText(text: String, maxPages: Int = 25): Boolean {
        if (visible(text)) return true
        val w = resources.displayMetrics.widthPixels; val h = resources.displayMetrics.heightPixels
        repeat(maxPages) {
            slowDrag(w / 2f, h * 0.70f, w / 2f, h * 0.35f)
            Thread.sleep(500)
            if (visible(text)) return true
        }
        return false
    }

    /** A drag that pauses at the end so the list stops where the finger stops. */
    fun slowDrag(x1: Float, y1: Float, x2: Float, y2: Float): Boolean {
        val move = Path().apply { moveTo(x1, y1); lineTo(x2, y2) }
        val hold = Path().apply { moveTo(x2, y2); lineTo(x2, y2 + 0.5f) }
        val s1 = GestureDescription.StrokeDescription(move, 0, 900, true)
        val s2 = s1.continueStroke(hold, 900, 250, false)
        val latch = CountDownLatch(1); var ok = false
        dispatchGesture(GestureDescription.Builder().addStroke(s1).build(), object : GestureResultCallback() {
            override fun onCompleted(d: GestureDescription?) {
                dispatchGesture(GestureDescription.Builder().addStroke(s2).build(), object : GestureResultCallback() {
                    override fun onCompleted(d: GestureDescription?) { ok = true; latch.countDown() }
                    override fun onCancelled(d: GestureDescription?) { latch.countDown() }
                }, null)
            }
            override fun onCancelled(d: GestureDescription?) { latch.countDown() }
        }, null)
        latch.await(3000, TimeUnit.MILLISECONDS)
        return ok
    }

    private fun scrollables(): List<AccessibilityNodeInfo> {
        val out = ArrayList<AccessibilityNodeInfo>()
        fun walk(n: AccessibilityNodeInfo?) {
            if (n == null) return
            if (n.isScrollable && n.isVisibleToUser) out.add(n)
            for (i in 0 until n.childCount) walk(n.getChild(i))
        }
        (windows?.mapNotNull { it.root } ?: listOfNotNull(rootInActiveWindow)).forEach { walk(it) }
        return out
    }

    companion object {
        @Volatile var instance: A11yService? = null
            private set
    }
}
