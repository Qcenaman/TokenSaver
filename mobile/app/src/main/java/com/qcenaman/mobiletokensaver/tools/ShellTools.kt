package com.qcenaman.mobiletokensaver.tools

import com.qcenaman.mobiletokensaver.App
import com.qcenaman.mobiletokensaver.engine.Jobs
import com.qcenaman.mobiletokensaver.engine.Shell
import com.qcenaman.mobiletokensaver.mcp.*

/**
 * run / run_job / job — shell commands as background jobs over our shell-user channel.
 * A call always comes back within its wait (default [DEFAULT_WAIT_S], under the PC's 60 s pass-through limit) with
 * whatever output exists so far; timeout_seconds (alias of wait_seconds) also kills the command when it is still
 * running at the end of the wait.
 */
object ShellTools {
    const val DEFAULT_WAIT_S = 45

    /** wait_seconds, else timeout_seconds, else [def]; never below 1. */
    fun waitSeconds(a: Map<String, Any?>, def: Int): Int = a.int("wait_seconds", a.int("timeout_seconds", def)).coerceAtLeast(1)

    val run = Tool("run", "Run a shell command as a background job; returns exit code, error lines and a short tail within wait_seconds (default 45; the job keeps running, see job status). timeout_seconds instead kills the command when it is still running at that point. Full log stays on the phone.",
        """{"type":"object","properties":{"command":{"type":"string"},"name":{"type":"string"},"tail":{"type":"integer"},"wait_seconds":{"type":"integer"},"timeout_seconds":{"type":"integer"},"keep_running":{"type":"boolean"}},"required":["command"]}""") { a ->
        val job = Jobs.start(a.string("name", "run"), a.string("command"))
        if (a.bool("keep_running")) return@Tool ToolResult.ok("job ${job.id} started (keep_running)")
        finish(job, a, DEFAULT_WAIT_S)
    }

    val runJob = Tool("run_job", "Run a sequence of shell commands as one background job (returns within wait_seconds, default 120; timeout_seconds kills it instead).",
        """{"type":"object","properties":{"commands":{"type":"array","items":{"type":"string"}},"name":{"type":"string"},"tail":{"type":"integer"},"wait_seconds":{"type":"integer"},"timeout_seconds":{"type":"integer"}},"required":["commands"]}""") { a ->
        val cmd = a.list("commands").joinToString(" && ") { "( ${it} )" }
        val job = Jobs.start(a.string("name", "run_job"), cmd)
        finish(job, a, 120)
    }

    val job = Tool("job", "Inspect jobs: action status/list/cancel.",
        """{"type":"object","properties":{"action":{"type":"string"},"id":{"type":"string"},"wait_seconds":{"type":"integer"},"tail":{"type":"integer"}}}""") { a ->
        when (a.string("action", "status").lowercase()) {
            "list" -> ToolResult.ok(Jobs.list().joinToString("\n") { "${it.id} ${it.name} ${if (it.done) "done exit=${it.exit}" else "running pid=${it.pid}"}" }.ifEmpty { "no jobs" })
            "cancel" -> ToolResult.ok(if (Jobs.cancel(App.ctx, a["id"]?.toString())) "cancelled" else "not found")
            else -> { val j = Jobs.get(a["id"]?.toString()) ?: return@Tool ToolResult.err("no job"); if (a.int("wait_seconds", 0) > 0) Jobs.waitFor(j, a.int("wait_seconds", 0)); report(j, a.int("tail", 30)) }
        }
    }

    /** Wait for the job (bounded), kill it when timeout_seconds asked for that, and report what there is. */
    private fun finish(job: Jobs.Job, a: Map<String, Any?>, def: Int): ToolResult {
        val wait = waitSeconds(a, def)
        Jobs.waitFor(job, wait)
        val killed = if (!job.done && a.int("timeout_seconds", 0) > 0) { Jobs.cancel(App.ctx, job.id); "killed after ${wait}s (timeout_seconds)" } else null
        return report(job, a.int("tail", 20), killed ?: if (job.done) null else "still running after ${wait}s; follow it with job status id=${job.id}")
    }

    private fun report(j: Jobs.Job, tail: Int, note: String? = null): ToolResult {
        val status = when {
            j.done -> "exit=${j.exit}"
            j.pid > 0 -> "running pid=${j.pid}"
            else -> "running (no shell channel yet: ${Shell.lastError.ifEmpty { "waiting for the helper/ADB" }})"
        }
        val errs = Jobs.errorLines(j)
        val sb = StringBuilder("job ${j.id} $status")
        if (note != null) sb.append(" — ").append(note)
        if (errs.isNotEmpty()) sb.append("\n-- errors --\n").append(errs)
        val t = Jobs.tail(j, tail)
        if (t.isNotEmpty()) sb.append("\n-- tail --\n").append(t)
        return ToolResult(sb.toString(), isError = !j.done || (j.exit ?: 0) != 0)
    }

    val all = listOf(run, runJob, job)
}
