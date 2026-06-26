package net.hotelpos.opsview

import android.content.Context

/** Thin SharedPreferences wrapper for the user-overridable start URL. */
class Prefs(ctx: Context) {
    private val sp = ctx.getSharedPreferences("opsview", Context.MODE_PRIVATE)

    fun startUrl(): String = sp.getString(KEY_START_URL, DEFAULT_START_URL) ?: DEFAULT_START_URL

    fun setStartUrl(value: String) {
        sp.edit().putString(KEY_START_URL, value.trim()).apply()
    }

    companion object {
        const val KEY_START_URL = "start_url"
        const val DEFAULT_START_URL = "https://ops.hotelpos.net/dashboard/wall"
    }
}
