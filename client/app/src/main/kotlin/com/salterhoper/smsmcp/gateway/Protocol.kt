package com.salterhoper.smsmcp.gateway

import java.net.URI
import java.net.URLDecoder
import java.security.MessageDigest

/**
 * Pure logic shared by the app, kept free of Android types so it can be unit
 * tested on the JVM. Wire format: docs/PROTOCOL.md.
 */

/** Job states on the phone. Same names and order as the server's statuses. */
object JobState {
    const val ACCEPTED = "accepted"   // stored locally, not yet handed to SmsManager
    const val SENDING = "sending"     // persisted BEFORE calling SmsManager
    const val UNKNOWN = "unknown"     // crashed mid-send; never retried automatically
    const val SENT = "sent"
    const val DELIVERED = "delivered"
    const val FAILED = "failed"

    private val rank = mapOf(ACCEPTED to 2, SENDING to 3, UNKNOWN to 4, SENT to 5, DELIVERED to 6, FAILED to 6)

    /** True if moving from [from] to [to] is a forward step. */
    fun canAdvance(from: String, to: String): Boolean = (rank[to] ?: -1) > (rank[from] ?: -1)
}

/** Parsed smsmcp://pair?url=...&token=... link from the QR code. */
data class PairingLink(val serverUrl: String, val token: String) {
    companion object {
        fun parse(raw: String): PairingLink? {
            val uri = runCatching { URI(raw.trim()) }.getOrNull() ?: return null
            if (uri.scheme != "smsmcp" || uri.host != "pair") return null
            val params = (uri.rawQuery ?: return null).split("&").mapNotNull {
                val i = it.indexOf('=')
                if (i <= 0) null else it.substring(0, i) to URLDecoder.decode(it.substring(i + 1), "UTF-8")
            }.toMap()
            val url = params["url"]?.trimEnd('/') ?: return null
            val token = params["token"] ?: return null
            if (!url.startsWith("http://") && !url.startsWith("https://")) return null
            return PairingLink(url, token)
        }
    }
}

/** ws(s):// URL for the device socket. */
fun wsUrl(serverUrl: String, path: String = "/v1/device/ws"): String =
    serverUrl.trimEnd('/').replaceFirst(Regex("^http"), "ws") + path

/**
 * Dedup key for an inbound SMS. The same SMS seen twice (receiver re-fires,
 * upload retried) hashes the same, so the server stores it once.
 */
fun inboundDedup(from: String, timestampMillis: Long, body: String): String {
    val md = MessageDigest.getInstance("SHA-256")
    md.update("$from\u001f$timestampMillis\u001f$body".toByteArray(Charsets.UTF_8))
    return md.digest().joinToString("") { "%02x".format(it) }
}
