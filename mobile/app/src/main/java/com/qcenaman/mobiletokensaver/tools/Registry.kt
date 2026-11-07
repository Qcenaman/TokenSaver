package com.qcenaman.mobiletokensaver.tools

import com.qcenaman.mobiletokensaver.mcp.Tool

/** All MCP tools the phone engine exposes. */
object Registry {
    val tools: List<Tool> = buildList {
        addAll(Fs.all)
        addAll(ShellTools.all)
        addAll(Ui.all)
        addAll(Device.all)
        addAll(Extra.all)
    }
}
