package net.hotelpos.opsview

import java.net.URI

/** Decides whether a navigation target stays in the WebView (same host as base). */
object UrlPolicy {
    private fun host(url: String): String? = try {
        URI(url).host
    } catch (e: Exception) {
        null
    }

    fun isInApp(baseUrl: String, target: String): Boolean {
        val b = host(baseUrl) ?: return false
        val t = host(target) ?: return false
        return t.equals(b, ignoreCase = true)
    }
}
