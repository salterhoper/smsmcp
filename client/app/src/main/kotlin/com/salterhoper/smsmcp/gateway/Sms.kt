package com.salterhoper.smsmcp.gateway

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Telephony
import android.telephony.SmsManager
import android.telephony.SmsMessage
import android.telephony.SubscriptionManager
import android.util.Log
import androidx.core.content.ContextCompat
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.asSharedFlow

private const val TAG = "smsmcp"

/** In-process signal from receivers to the running service: "something to report". */
object GatewayEvents {
    private val _changes = MutableSharedFlow<Unit>(extraBufferCapacity = 64)
    val changes = _changes.asSharedFlow()
    fun poke() { _changes.tryEmit(Unit) }
}

data class SimInfo(val subscriptionId: Int, val label: String, val number: String?)

object Sims {
    @SuppressLint("MissingPermission", "HardwareIds")
    fun list(context: Context): List<SimInfo> {
        if (ContextCompat.checkSelfPermission(context, Manifest.permission.READ_PHONE_STATE) != PackageManager.PERMISSION_GRANTED) {
            return emptyList()
        }
        val sm = context.getSystemService(SubscriptionManager::class.java) ?: return emptyList()
        return (sm.activeSubscriptionInfoList ?: emptyList()).map { info ->
            val number = runCatching {
                if (Build.VERSION.SDK_INT >= 33) sm.getPhoneNumber(info.subscriptionId) else @Suppress("DEPRECATION") info.number
            }.getOrNull()?.takeIf { it.isNotBlank() }
            SimInfo(info.subscriptionId, "SIM ${info.simSlotIndex + 1} · ${info.carrierName ?: info.displayName}", number)
        }
    }
}

object SmsSender {
    const val ACTION_SENT = "com.salterhoper.smsmcp.gateway.SMS_SENT"
    const val ACTION_DELIVERED = "com.salterhoper.smsmcp.gateway.SMS_DELIVERED"
    const val EXTRA_ID = "message_id"

    private fun manager(context: Context, subId: Int): SmsManager {
        val base = context.getSystemService(SmsManager::class.java)
        return when {
            subId < 0 -> base
            Build.VERSION.SDK_INT >= 31 -> base.createForSubscriptionId(subId)
            else -> @Suppress("DEPRECATION") SmsManager.getSmsManagerForSubscriptionId(subId)
        }
    }

    fun partCount(context: Context, subId: Int, body: String): Int = manager(context, subId).divideMessage(body).size

    /**
     * Hands the SMS to the carrier. The caller must already have persisted the
     * job as "sending" (JobStore.markSending) so a crash here never leads to a
     * second send. Results arrive in [SmsStatusReceiver].
     */
    fun send(context: Context, subId: Int, id: String, to: String, body: String) {
        val sm = manager(context, subId)
        val parts = sm.divideMessage(body)
        val sent = ArrayList<PendingIntent>()
        val delivered = ArrayList<PendingIntent>()
        parts.indices.forEach { i ->
            sent += pending(context, ACTION_SENT, id, i)
            delivered += pending(context, ACTION_DELIVERED, id, i)
        }
        if (parts.size == 1) sm.sendTextMessage(to, null, body, sent[0], delivered[0])
        else sm.sendMultipartTextMessage(to, null, parts, sent, delivered)
    }

    private fun pending(context: Context, action: String, id: String, part: Int): PendingIntent {
        val intent = Intent(context, SmsStatusReceiver::class.java)
            .setAction(action)
            .setData(android.net.Uri.parse("smsmcp://$action/$id/$part")) // unique per part, so intents don't merge
            .putExtra(EXTRA_ID, id)
        return PendingIntent.getBroadcast(context, 0, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_ONE_SHOT)
    }
}

/** Carrier results for each part of an outbound SMS. */
class SmsStatusReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val id = intent.getStringExtra(SmsSender.EXTRA_ID) ?: return
        val store = JobStore.get(context)
        val newState = when (intent.action) {
            SmsSender.ACTION_SENT -> {
                val ok = resultCode == Activity.RESULT_OK
                store.partResult(id, delivered = false, ok = ok, error = if (ok) null else "carrier rejected the SMS (code $resultCode)")
            }
            SmsSender.ACTION_DELIVERED -> {
                val pdu = intent.getByteArrayExtra("pdu")
                val format = intent.getStringExtra("format")
                // TP-Status: 0 = delivered, 0x20..0x3F temporary (still trying), >= 0x40 permanent failure.
                val status = pdu?.let { SmsMessage.createFromPdu(it, format)?.status } ?: 0
                when {
                    status < 0x20 -> store.partResult(id, delivered = true, ok = true, error = null)
                    status >= 0x40 -> store.partResult(id, delivered = true, ok = false, error = "delivery failed (TP-Status $status)")
                    else -> null
                }
            }
            else -> null
        }
        if (newState != null) Log.i(TAG, "$id -> $newState")
        GatewayEvents.poke()
    }
}

/** Incoming SMS on the chosen SIM. Stored first, uploaded by the service. */
class SmsReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Telephony.Sms.Intents.SMS_RECEIVED_ACTION) return
        val prefs = Prefs(context)
        if (!prefs.isPaired) return

        val subId = intent.getIntExtra(SubscriptionManager.EXTRA_SUBSCRIPTION_INDEX, intent.getIntExtra("subscription", -1))
        if (prefs.subscriptionId >= 0 && subId >= 0 && subId != prefs.subscriptionId) return // other SIM

        val messages = Telephony.Sms.Intents.getMessagesFromIntent(intent) ?: return
        // A multipart SMS arrives as several PDUs in one intent: join them per sender.
        messages.groupBy { it.displayOriginatingAddress ?: "" }.forEach { (from, parts) ->
            if (from.isEmpty()) return@forEach
            val body = parts.joinToString("") { it.displayMessageBody ?: "" }
            val ts = parts.first().timestampMillis
            JobStore.get(context).insertInbound(inboundDedup(from, ts, body), from, body, ts)
        }
        GatewayEvents.poke()
    }
}

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val prefs = Prefs(context)
        if (prefs.isPaired && prefs.gatewayEnabled) GatewayService.start(context)
    }
}
