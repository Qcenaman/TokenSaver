package com.qcenaman.mobiletokensaver.net

import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

/**
 * The shared peer registry's pure logic (same rules as the computers' mesh.go): one entry per device
 * (ip = Tailscale, lan, mac, port, token, kind computer|android, tunnel, adb, paired, updated). The newest
 * announcement of a device wins; fields it leaves empty keep their old value; this device is never stored.
 * Pure Kotlin so it is unit-testable; Peers keeps the map in Prefs.
 */
object PeerRegistry {
    const val ANDROID_PORT = 8765
    const val COMPUTER_PORT = 8766
    const val UNREACHABLE = "peer-unreachable: "
    private val KEEP = listOf("ip", "lan", "mac", "kind", "tunnel", "adb")

    fun norm(n: String) = n.trim().lowercase()

    /** Go's time.Time JSON (RFC 3339, up to 9 fraction digits) → epoch ms; 0 when missing or unreadable. */
    fun parseTime(v: Any?): Long {
        val s = v?.toString().orEmpty()
        if (s.isEmpty()) return 0
        return runCatching { OffsetDateTime.parse(s).toInstant().toEpochMilli() }.getOrDefault(0L)
    }

    fun goTime(ms: Long): String = DateTimeFormatter.ISO_OFFSET_DATE_TIME.format(Instant.ofEpochMilli(ms).atOffset(ZoneOffset.UTC))

    fun effTime(e: Map<String, Any?>): Long = maxOf(parseTime(e["updated"]), parseTime(e["paired"]))

    private fun str(e: Map<String, Any?>, k: String) = e[k]?.toString().orEmpty()

    fun port(e: Map<String, Any?>): Int = when (val v = e["port"]) {
        is Number -> v.toInt(); is String -> v.toIntOrNull() ?: 0; else -> 0
    }.let { if (it > 0) it else if (str(e, "kind") == "android") ANDROID_PORT else COMPUTER_PORT }

    /** Fold incoming entries into m; returns the names that changed (sorted). */
    fun merge(m: MutableMap<String, Map<String, Any?>>, incoming: Map<String, Any?>, self: String): List<String> {
        val me = norm(self)
        val changed = ArrayList<String>()
        for ((rawName, v) in incoming) {
            val name = norm(rawName)
            @Suppress("UNCHECKED_CAST") val e = (v as? Map<String, Any?>) ?: continue
            if (name.isEmpty() || name == me || str(e, "token").isEmpty()) continue
            val cur = m[name]
            if (cur != null && effTime(e) <= effTime(cur)) continue
            val out = LinkedHashMap(e)
            if (cur != null) for (k in KEEP) if (str(out, k).isEmpty() && str(cur, k).isNotEmpty()) out[k] = cur[k]
            if (((out["port"] as? Number)?.toInt() ?: 0) <= 0)
                out["port"] = (cur?.get("port") as? Number)?.toInt()?.takeIf { it > 0 } ?: port(out)
            if (cur == out) continue
            m[name] = out
            changed += name
        }
        return changed.sorted()
    }

    fun routes(e: Map<String, Any?>): List<Routes.Route> =
        Routes.of(str(e, "lan"), str(e, "ip"), "", str(e, "tunnel"), port(e))

    /** Who to relay through: the primary computer first, then Android devices (always on), then other computers. */
    fun relays(target: String, self: String, m: Map<String, Map<String, Any?>>, primary: String): List<String> =
        m.filter { (n, e) -> n != target && n != self && str(e, "token").isNotEmpty() }.keys
            .sortedWith(compareBy<String>({ if (it == primary) 0 else if (str(m[it]!!, "kind") == "android") 1 else 2 }, { it }))
            .take(5)

    /** The relay could not reach the target, or runs a version that cannot relay. */
    fun relayUnable(msg: String) = UNREACHABLE in msg || "not its peer tool" in msg || "unknown tool" in msg ||
        "not in this node's registry" in msg || "token rejected" in msg || "rejected the stored token" in msg

    /** Old one-off link files (/data/local/tmp/<x>-tokensaver.json) → name to entry, or null. */
    fun fromLinkFile(fileName: String, m: Map<String, Any?>): Pair<String, Map<String, Any?>>? {
        fun s(vararg ks: String) = ks.firstNotNullOfOrNull { k -> m[k]?.toString()?.takeIf { it.isNotEmpty() } }.orEmpty()
        val tok = s("token"); if (tok.isEmpty()) return null
        val name = norm(s("name", "device").ifEmpty { fileName.substringAfterLast('/').removeSuffix(".json").removeSuffix("-tokensaver") })
        if (name.isEmpty()) return null
        val e = linkedMapOf<String, Any?>("ip" to s("tailscale", "tailscale_ip", "ip"), "lan" to s("lan", "lan_ip"), "token" to tok,
            "port" to (s("port").toIntOrNull() ?: COMPUTER_PORT), "kind" to "computer", "paired" to goTime(0))
        return name to e
    }
}
