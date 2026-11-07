package com.qcenaman.mobiletokensaver

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Command log: kept in memory for the UI and appended to a file (never truncated). */
object Log {
    private lateinit var file: File
    val recent = MutableStateFlow<List<String>>(emptyList())
    private val fmt = SimpleDateFormat("MM-dd HH:mm:ss", Locale.US)

    fun init(ctx: Context) {
        file = File(ctx.filesDir, "logs/engine.log").apply { parentFile?.mkdirs() }
    }

    @Synchronized
    fun add(msg: String) {
        val line = "${fmt.format(Date())} $msg"
        recent.value = (recent.value + line).takeLast(300)
        runCatching { file.appendText(line.take(2000) + "\n") }
    }
}
