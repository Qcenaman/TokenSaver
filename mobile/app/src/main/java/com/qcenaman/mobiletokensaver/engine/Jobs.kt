package com.qcenaman.mobiletokensaver.engine

import android.content.Context
import com.qcenaman.mobiletokensaver.App
import java.io.File
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicInteger
import kotlin.concurrent.thread

/** Background shell jobs, mirroring the PC's run/run_job/job model. Full logs stay on the phone. */
object Jobs {
    data class Job(
        val id: String,
        val name: String,
        @Volatile var pid: Int = -1,
        @Volatile var exit: Int? = null,
        @Volatile var done: Boolean = false,
        val log: File,
        @Volatile var cancelled: Boolean = false
    )

    private val jobs = ConcurrentHashMap<String, Job>()
    private val seq = AtomicInteger(0)
    private fun dir(): File = File(App.ctx.filesDir, "jobs").also { it.mkdirs() }

    fun start(name: String, command: String): Job {
        val id = "%d-%03d".format(System.currentTimeMillis() % 100000000, seq.incrementAndGet())
        val log = File(dir(), "$id.log")
        val job = Job(id, name, log = log)
        jobs[id] = job
        thread(name = "job-$id") {
            try {
                log.outputStream().use { out ->
                    val code = Shell.exec(App.ctx, command, onPid = { job.pid = it }) { b, n ->
                        if (!job.cancelled) out.write(b, 0, n)
                    }
                    job.exit = code
                }
            } catch (e: Exception) {
                log.appendText("error: ${e.message}\n")
                if (job.exit == null) job.exit = -1
            } finally { job.done = true }
        }
        return job
    }

    fun get(id: String?): Job? = if (id == null) jobs.values.maxByOrNull { it.id } else jobs[id]
    fun list(): List<Job> = jobs.values.sortedByDescending { it.id }

    fun cancel(ctx: Context, id: String?): Boolean {
        val j = get(id) ?: return false
        j.cancelled = true
        if (j.pid > 0) Shell.run(ctx, "kill -9 ${j.pid} 2>/dev/null; kill -9 -${j.pid} 2>/dev/null")
        j.done = true; if (j.exit == null) j.exit = 137
        return true
    }

    fun waitFor(j: Job, seconds: Int): Job {
        val end = System.currentTimeMillis() + seconds * 1000L
        while (!j.done && System.currentTimeMillis() < end) Thread.sleep(120)
        return j
    }

    fun tail(j: Job, lines: Int): String {
        if (!j.log.exists()) return ""
        val all = j.log.readLines()
        return all.takeLast(lines).joinToString("\n")
    }

    fun errorLines(j: Job, max: Int = 8): String {
        if (!j.log.exists()) return ""
        return j.log.readLines().filter {
            it.contains("error", true) || it.contains("exception", true) ||
                it.contains("fail", true) || it.contains("not found", true) || it.contains("denied", true)
        }.takeLast(max).joinToString("\n")
    }
}
