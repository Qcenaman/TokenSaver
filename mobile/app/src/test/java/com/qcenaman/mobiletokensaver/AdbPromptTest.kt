package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.service.AdbPrompt
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.MessageDigest
import java.util.Base64

class AdbPromptTest {
    private val keyBytes = ByteArray(64) { it.toByte() }
    private val pub = Base64.getEncoder().encodeToString(keyBytes) + " me@PC"
    private val expected = MessageDigest.getInstance("MD5").digest(keyBytes).joinToString(":") { "%02X".format(it) }

    @Test fun fingerprintMatchesAndroidFormat() = assertEquals(expected, AdbPrompt.fingerprint(pub))

    @Test fun normalizeAcceptsKeyOrFingerprint() {
        assertEquals(expected, AdbPrompt.normalize(pub))
        assertEquals(expected, AdbPrompt.normalize(expected.lowercase()))
        assertNull(AdbPrompt.normalize("  "))
    }

    @Test fun findsFingerprintInDialogText() {
        val texts = listOf("Allow USB debugging?", "The computer's RSA key fingerprint is:\n$expected", "Allow")
        assertEquals(expected, AdbPrompt.findFingerprint(texts))
    }

    @Test fun approvesOnlyTrustedKeys() {
        assertTrue(AdbPrompt.shouldApprove(expected, setOf(expected.lowercase())))
        assertFalse(AdbPrompt.shouldApprove(expected, setOf("00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF")))
        assertFalse(AdbPrompt.shouldApprove(null, setOf(expected)))
        assertFalse(AdbPrompt.shouldApprove(expected, emptySet()))
    }

    @Test fun recognisesOnlySystemUiDebugPrompt() {
        assertTrue(AdbPrompt.isAdbPrompt("com.android.systemui", "com.android.systemui.usb.UsbDebuggingActivity"))
        assertFalse(AdbPrompt.isAdbPrompt("com.evil.app", "UsbDebuggingActivity"))
        assertFalse(AdbPrompt.isAdbPrompt("com.android.systemui", "com.android.systemui.Other"))
    }
}
