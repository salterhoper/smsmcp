package com.salterhoper.smsmcp.gateway

import android.content.Context
import android.content.SharedPreferences

/** Pairing result and settings. The device token is the phone's only credential. */
class Prefs(context: Context) {
    private val sp: SharedPreferences = context.getSharedPreferences("gateway", Context.MODE_PRIVATE)

    var serverUrl: String?
        get() = sp.getString("server_url", null)
        set(v) = sp.edit().putString("server_url", v).apply()

    var deviceId: String?
        get() = sp.getString("device_id", null)
        set(v) = sp.edit().putString("device_id", v).apply()

    var deviceToken: String?
        get() = sp.getString("device_token", null)
        set(v) = sp.edit().putString("device_token", v).apply()

    var phoneNumber: String?
        get() = sp.getString("phone_number", null)
        set(v) = sp.edit().putString("phone_number", v).apply()

    /** SubscriptionManager id of the chosen SIM; -1 = system default. */
    var subscriptionId: Int
        get() = sp.getInt("subscription_id", -1)
        set(v) = sp.edit().putInt("subscription_id", v).apply()

    /** "smsmcp verify 123456" until the verification SMS has been sent. */
    var pendingVerifyMessage: String?
        get() = sp.getString("pending_verify", null)
        set(v) = sp.edit().putString("pending_verify", v).apply()

    var gatewayEnabled: Boolean
        get() = sp.getBoolean("enabled", false)
        set(v) = sp.edit().putBoolean("enabled", v).apply()

    val isPaired: Boolean get() = serverUrl != null && deviceToken != null

    fun clear() = sp.edit().clear().apply()
}
