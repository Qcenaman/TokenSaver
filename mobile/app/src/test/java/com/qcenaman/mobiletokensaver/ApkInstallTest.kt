package com.qcenaman.mobiletokensaver

import com.qcenaman.mobiletokensaver.tools.ApkInstall
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class ApkInstallTest {
    @Test fun installCmdQuotesAndDefaultsToReplace() {
        assertEquals("pm install -r '/data/local/tmp/mts-x.apk'", ApkInstall.installCmd("/data/local/tmp/mts-x.apk"))
        assertEquals("pm install -r -t '/data/local/tmp/a b.apk'", ApkInstall.installCmd("/data/local/tmp/a b.apk", test = true))
        assertEquals("pm install '/x.apk'", ApkInstall.installCmd("/x.apk", replace = false))
    }

    @Test fun quotingSurvivesShellMetacharacters() {
        assertEquals("'it'\\''s; rm -rf /'", ApkInstall.q("it's; rm -rf /"))
        assertEquals("am force-stop 'com.x; reboot'", ApkInstall.stopCmd("com.x; reboot"))
        assertEquals("pm uninstall 'com.example'", ApkInstall.uninstallCmd("com.example"))
        assertTrue(ApkInstall.launchCmd("com.example").startsWith("monkey -p 'com.example' "))
        assertTrue(ApkInstall.stagedPath("abc").startsWith("/data/local/tmp/mts-"))
    }

    @Test fun sha256MismatchIsAnError() {
        val f = File.createTempFile("apk", ".apk").apply { writeText("hello"); deleteOnExit() }
        val good = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
        assertNull(ApkInstall.verify(f, 5, good))
        assertNull(ApkInstall.verify(f, null, good.uppercase()))
        assertTrue(ApkInstall.verify(f, null, "00" + good.drop(2))!!.startsWith("sha256 mismatch"))
        assertTrue(ApkInstall.verify(f, 6, null)!!.startsWith("size mismatch"))
    }

    @Test fun pmOutputParsing() {
        assertNull(ApkInstall.failure("Performing Streamed Install\nSuccess\n", 0))
        assertEquals("Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: signatures do not match]",
            ApkInstall.failure("Performing Streamed Install\nFailure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: signatures do not match]\n", 1))
        assertTrue(ApkInstall.failure("", 1)!!.contains("exit 1"))
    }

    @Test fun permissionAndAppopsCommands() {
        assertEquals("appops set --uid 'com.xiaoji.egggame' 'MANAGE_EXTERNAL_STORAGE' 'allow'",
            ApkInstall.appopsCmd("com.xiaoji.egggame", "MANAGE_EXTERNAL_STORAGE", "allow", true))
        assertEquals("appops get 'p' 'OP'", ApkInstall.appopsCmd("p", "OP", "", true))
        assertEquals("pm grant 'p' 'android.permission.CAMERA'", ApkInstall.grantCmd("p", "CAMERA"))
        assertEquals("pm revoke 'p' 'com.x.PERM'", ApkInstall.revokeCmd("p", "com.x.PERM"))
    }

    @Test fun helperLaunchLineKeepsSecretAndLoop() {
        val c = com.qcenaman.mobiletokensaver.engine.HelperClient.launchCmd("com.qcenaman.mobiletokensaver", "s3cret")
        assertTrue(c.contains("helper.Main s3cret; sleep 5; done"))
        assertTrue(c.contains("nohup setsid sh -c 'while :; do CLASSPATH=$(pm path com.qcenaman.mobiletokensaver"))
    }
}
