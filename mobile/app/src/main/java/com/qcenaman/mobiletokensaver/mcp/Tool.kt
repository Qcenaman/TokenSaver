package com.qcenaman.mobiletokensaver.mcp

/** One MCP tool: its name, its JSON Schema (as a raw JSON string), and its handler. */
class Tool(
    val name: String,
    val description: String,
    val schema: String,
    val handler: (args: Map<String, Any?>) -> ToolResult
)

class ToolResult(val text: String, val isError: Boolean = false, val imageB64: String? = null, val mime: String = "image/jpeg") {
    companion object {
        fun ok(text: String) = ToolResult(text)
        fun err(text: String) = ToolResult(text, isError = true)
        fun image(b64: String, mime: String = "image/jpeg", caption: String = "") = ToolResult(caption, imageB64 = b64, mime = mime)
    }
}
