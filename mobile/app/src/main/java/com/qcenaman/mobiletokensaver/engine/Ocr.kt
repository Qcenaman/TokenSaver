package com.qcenaman.mobiletokensaver.engine

import android.accessibilityservice.AccessibilityService
import android.graphics.Bitmap
import android.graphics.Rect
import android.view.Display
import com.google.android.gms.tasks.Tasks
import com.google.mlkit.vision.common.InputImage
import com.google.mlkit.vision.text.TextRecognition
import com.google.mlkit.vision.text.latin.TextRecognizerOptions
import com.qcenaman.mobiletokensaver.service.A11yService
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** Screenshot (through accessibility, no shell) + on-device ML Kit OCR. Call off the main thread. */
object Ocr {
    data class Line(val text: String, val bounds: Rect)

    private val recognizer by lazy { TextRecognition.getClient(TextRecognizerOptions.DEFAULT_OPTIONS) }
    private val exec = Executors.newSingleThreadExecutor()

    fun screenshot(svc: A11yService): Bitmap? {
        val latch = CountDownLatch(1)
        var bmp: Bitmap? = null
        runCatching {
            svc.takeScreenshot(Display.DEFAULT_DISPLAY, exec, object : AccessibilityService.TakeScreenshotCallback {
                override fun onSuccess(r: AccessibilityService.ScreenshotResult) {
                    runCatching {
                        val hw = Bitmap.wrapHardwareBuffer(r.hardwareBuffer, r.colorSpace)
                        bmp = hw?.copy(Bitmap.Config.ARGB_8888, false)
                        hw?.recycle()
                    }
                    r.hardwareBuffer.close()
                    latch.countDown()
                }
                override fun onFailure(code: Int) { latch.countDown() }
            })
        }.onFailure { latch.countDown() }
        latch.await(4, TimeUnit.SECONDS)
        return bmp
    }

    /** Every text line on screen with its bounds; empty when the screenshot or OCR failed. */
    fun lines(svc: A11yService): List<Line> {
        val bmp = screenshot(svc) ?: return emptyList()
        return try {
            val res = Tasks.await(recognizer.process(InputImage.fromBitmap(bmp, 0)), 8, TimeUnit.SECONDS)
            res.textBlocks.flatMap { b -> b.lines.mapNotNull { l -> l.boundingBox?.let { Line(l.text, it) } } }
        } catch (_: Throwable) { emptyList() } finally { bmp.recycle() }
    }

    fun find(svc: A11yService, text: String): Line? {
        val ls = lines(svc)
        return ls.firstOrNull { it.text.equals(text, true) } ?: ls.firstOrNull { it.text.contains(text, true) }
    }
}
