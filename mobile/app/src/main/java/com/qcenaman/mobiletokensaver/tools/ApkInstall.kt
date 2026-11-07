package com.qcenaman.mobiletokensaver.tools

import java.io.File
import java.io.InputStream
import java.security.MessageDigest

/** Pure helpers for the apps tool's install path: quoting, pm command building, download checks, result parsing. */
object ApkInstall {
    const val STAGE_DIR = "/data/local/tmp"

    /** Single-quote for sh: 'a'\''b'. */
    fun q(s: String): String = "'" + s.replace("'", "'\\''") + "'"

    fun stagedPath(token: String): String = "$STAGE_DIR/mts-$token.apk"

    fun installCmd(staged: String, replace: Boolean = true, test: Boolean = false): String =
        buildString {
            append("pm install")
            if (replace) append(" -r")
            if (test) append(" -t")
            append(' ').append(q(staged))
        }

    fun launchCmd(pkg: String) = "monkey -p ${q(pkg)} -c android.intent.category.LAUNCHER 1"
    fun stopCmd(pkg: String) = "am force-stop ${q(pkg)}"
    fun uninstallCmd(pkg: String) = "pm uninstall ${q(pkg)}"

    /** Permissions may be given short ("CAMERA") or full ("android.permission.CAMERA"). */
    fun fullPermission(p: String): String = if (p.contains('.')) p else "android.permission.$p"
    fun grantCmd(pkg: String, perm: String) = "pm grant ${q(pkg)} ${q(fullPermission(perm))}"
    fun revokeCmd(pkg: String, perm: String) = "pm revoke ${q(pkg)} ${q(fullPermission(perm))}"

    /** appops set/get; uid=true applies the mode to the whole uid (needed for MANAGE_EXTERNAL_STORAGE). */
    fun appopsCmd(pkg: String, op: String, mode: String, uid: Boolean): String =
        if (mode.isEmpty()) "appops get ${q(pkg)}" + (if (op.isNotEmpty()) " ${q(op)}" else "")
        else "appops set" + (if (uid) " --uid" else "") + " ${q(pkg)} ${q(op)} ${q(mode)}"

    val MODES = setOf("allow", "deny", "ignore", "default", "foreground")

    /** Shell steps: copy [src] to [staged], print its size so the caller can check the copy was complete. */
    fun stageCmd(src: String, staged: String) = "cat ${q(src)} > ${q(staged)} && chmod 644 ${q(staged)} && stat -c %s ${q(staged)}"
    fun cleanupCmd(staged: String) = "rm -f ${q(staged)}"

    fun isHttpUrl(s: String) = s.startsWith("http://", true) || s.startsWith("https://", true)

    /** Hex sha256 of a stream. */
    fun sha256(input: InputStream): String {
        val md = MessageDigest.getInstance("SHA-256")
        val buf = ByteArray(64 * 1024)
        while (true) { val n = input.read(buf); if (n < 0) break; md.update(buf, 0, n) }
        return md.digest().joinToString("") { "%02x".format(it) }
    }

    /** Null when the file matches the optional size and sha256; otherwise an error message. */
    fun verify(file: File, expectedSize: Long?, expectedSha256: String?): String? {
        if (!file.exists() || file.length() == 0L) return "downloaded file is empty"
        if (expectedSize != null && expectedSize > 0 && file.length() != expectedSize)
            return "size mismatch: expected $expectedSize, got ${file.length()}"
        val want = expectedSha256?.trim()?.lowercase().orEmpty()
        if (want.isNotEmpty()) {
            val got = file.inputStream().use { sha256(it) }
            if (got != want) return "sha256 mismatch: expected $want, got $got"
        }
        return null
    }

    /** First INSTALL_FAILED_* / Failure line from pm output, or null when pm reported Success. */
    fun failure(pmOutput: String, exitCode: Int): String? {
        val lines = pmOutput.lines().map { it.trim() }.filter { it.isNotEmpty() }
        lines.firstOrNull { it.contains("INSTALL_FAILED_") || it.contains("INSTALL_PARSE_FAILED_") }?.let { return it }
        if (lines.any { it == "Success" }) return null
        return lines.firstOrNull { it.startsWith("Failure") || it.startsWith("Error") }
            ?: if (exitCode != 0) "pm install failed (exit $exitCode): ${pmOutput.trim().ifEmpty { "no output" }}" else "pm install gave no Success line: ${pmOutput.trim()}"
    }
}
