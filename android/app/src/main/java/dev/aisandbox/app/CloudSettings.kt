package dev.aisandbox.app

import android.content.Context
import java.net.URI

data class CloudSettings(val baseUrl: String, val token: String) {
    val enabled: Boolean
        get() = token.isNotBlank() && validUrl(baseUrl)

    companion object {
        private const val PREFS = "cloud_session"
        val DEFAULT_URL = BuildConfig.CLOUD_URL

        fun load(context: Context): CloudSettings {
            val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            if (prefs.getBoolean("signed_out", false)) {
                return CloudSettings(DEFAULT_URL, "")
            }
            val stored = CloudSettings(
                baseUrl = normalizeUrl(prefs.getString("base_url", DEFAULT_URL)),
                token = prefs.getString("token", "")?.trim().orEmpty(),
            )
            if (stored.enabled) {
                return stored
            }
            return signIn(context)
        }

        fun signIn(context: Context): CloudSettings {
            val settings = CloudSettings(DEFAULT_URL, BuildConfig.CLOUD_TOKEN.trim())
            context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
                .putBoolean("signed_out", false)
                .putString("base_url", settings.baseUrl)
                .putString("token", settings.token)
                .apply()
            return settings
        }

        fun signOut(context: Context) {
            context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
                .putBoolean("signed_out", true)
                .putString("base_url", DEFAULT_URL)
                .putString("token", "")
                .apply()
        }

        private fun normalizeUrl(raw: String?): String {
            val value = raw?.trim().orEmpty().ifBlank { DEFAULT_URL }
            val host = runCatching { URI(value).host }.getOrNull()?.lowercase()
            return if (host == "10.0.2.2" || host == "127.0.0.1" || host == "localhost") DEFAULT_URL else value
        }

        fun validUrl(raw: String): Boolean {
            val uri = runCatching { URI(raw.trim()) }.getOrNull() ?: return false
            if (uri.scheme != "http" && uri.scheme != "https") return false
            if (uri.host.isNullOrBlank() || uri.userInfo != null) return false
            return uri.path.isNullOrBlank() || uri.path == "/"
        }
    }
}
