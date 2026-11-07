package com.qcenaman.mobiletokensaver.mcp

// Minimal JSON — no external dependency, enough for MCP framing and tool args.
object Json {
    fun esc(s: String): String {
        val b = StringBuilder(s.length + 8)
        for (c in s) when (c) {
            '"' -> b.append("\\\""); '\\' -> b.append("\\\\")
            '\n' -> b.append("\\n"); '\r' -> b.append("\\r"); '\t' -> b.append("\\t")
            else -> if (c < ' ') b.append("\\u%04x".format(c.code)) else b.append(c)
        }
        return b.toString()
    }
    fun str(s: String) = "\"${esc(s)}\""

    fun parse(s: String): Any? = Parser(s).parseValue()

    fun encode(v: Any?): String = when (v) {
        null -> "null"
        is String -> str(v)
        is Boolean -> v.toString()
        is Number -> v.toString().removeSuffix(".0")
        is Map<*, *> -> v.entries.joinToString(",", "{", "}") { str(it.key.toString()) + ":" + encode(it.value) }
        is List<*> -> v.joinToString(",", "[", "]") { encode(it) }
        else -> str(v.toString())
    }

    class Parser(private val s: String) {
        private var i = 0
        fun parseValue(): Any? {
            ws()
            return when (s[i]) {
                '{' -> obj(); '[' -> arr(); '"' -> pstr()
                't' -> { i += 4; true }; 'f' -> { i += 5; false }; 'n' -> { i += 4; null }
                else -> num()
            }
        }
        private fun ws() { while (i < s.length && s[i].isWhitespace()) i++ }
        private fun obj(): Map<String, Any?> {
            val m = LinkedHashMap<String, Any?>(); i++; ws()
            if (s[i] == '}') { i++; return m }
            while (true) {
                ws(); val k = pstr(); ws(); i++ // colon
                m[k] = parseValue(); ws()
                if (s[i] == ',') { i++; continue }
                i++; break
            }
            return m
        }
        private fun arr(): List<Any?> {
            val l = ArrayList<Any?>(); i++; ws()
            if (s[i] == ']') { i++; return l }
            while (true) {
                l.add(parseValue()); ws()
                if (s[i] == ',') { i++; continue }
                i++; break
            }
            return l
        }
        private fun pstr(): String {
            val b = StringBuilder(); i++
            while (s[i] != '"') {
                if (s[i] == '\\') {
                    i++
                    when (s[i]) {
                        'n' -> b.append('\n'); 'r' -> b.append('\r'); 't' -> b.append('\t')
                        'b' -> b.append('\b'); 'f' -> b.append('\u000C'); '/' -> b.append('/')
                        '"' -> b.append('"'); '\\' -> b.append('\\')
                        'u' -> { b.append(s.substring(i + 1, i + 5).toInt(16).toChar()); i += 4 }
                        else -> b.append(s[i])
                    }
                } else b.append(s[i])
                i++
            }
            i++
            return b.toString()
        }
        private fun num(): Any {
            val st = i
            while (i < s.length && (s[i].isDigit() || s[i] in "-+.eE")) i++
            val t = s.substring(st, i)
            return t.toLongOrNull() ?: t.toDouble()
        }
    }
}

@Suppress("UNCHECKED_CAST")
fun Any?.asMap(): Map<String, Any?> = this as? Map<String, Any?> ?: emptyMap()
fun Map<String, Any?>.string(k: String, def: String = ""): String = this[k]?.toString() ?: def
fun Map<String, Any?>.int(k: String, def: Int): Int = when (val v = this[k]) {
    is Number -> v.toInt(); is String -> v.toIntOrNull() ?: def; else -> def
}
fun Map<String, Any?>.bool(k: String, def: Boolean = false): Boolean = when (val v = this[k]) {
    is Boolean -> v; is String -> v == "true"; else -> def
}
@Suppress("UNCHECKED_CAST")
fun Map<String, Any?>.list(k: String): List<Any?> = this[k] as? List<Any?> ?: emptyList()
