package com.qcenaman.mobiletokensaver.engine

/** Pulls the 6-digit code and IP:port out of the text of the "Pair device with pairing code" dialog. */
object PairText {
    data class Found(val code: String?, val host: String?, val port: Int)

    private val wholeCode = Regex("""^\s*(\d{3})\s?(\d{3})\s*$""")
    private val looseCode = Regex("""(?<![\d.:])(\d{6})(?![\d.:])""")
    private val hostPort = Regex("""(\d{1,3}(?:\.\d{1,3}){3})\s*:\s*(\d{2,5})""")

    fun parse(lines: List<String>): Found {
        var host: String? = null; var port = 0
        for (l in lines) hostPort.find(l)?.let { host = it.groupValues[1]; port = it.groupValues[2].toInt() }
        val code = lines.firstNotNullOfOrNull { l -> wholeCode.find(l)?.let { it.groupValues[1] + it.groupValues[2] } }
            ?: lines.firstNotNullOfOrNull { l -> if (hostPort.containsMatchIn(l)) null else looseCode.find(l)?.groupValues?.get(1) }
        return Found(code, host, port)
    }
}
