package com.qcenaman.mobiletokensaver

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Command log: every tool call and engine event, kept in the app and appended to a file (never trimmed on disk). */
object CmdLog {
    private lateinit var file: File
    private val fmt = SimpleDateFormat("MM-dd HH:mm:ss", Locale.US)
    val recent = MutableStateFlow<List<String>>(emptyList())

    fun init(c: Context) {
        file = File(c.filesDir, "logs/commands.log").also { it.parentFile?.mkdirs() }
    }

    @Synchronized
    fun add(msg: String) {
        val line = "${fmt.format(Date())} ${msg.replace('\n', ' ').take(300)}"
        try { file.appendText(line + "\n") } catch (_: Exception) {}
        recent.value = (listOf(line) + recent.value).take(200)
    }
}
