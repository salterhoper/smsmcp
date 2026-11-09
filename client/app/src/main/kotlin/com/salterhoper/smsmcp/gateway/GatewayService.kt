package com.salterhoper.smsmcp.gateway

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.BatteryManager
import android.os.Build
import android.util.Log
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import kotlin.math.min

/**
 * Keeps the WebSocket to the server open, sends queued jobs through the
 * chosen SIM, and reports results. Runs as a foreground service so the OS
 * does not kill it.
 *
 * Every report (status, inbound) is re-sent on each reconnect and on each
 * change until the server acks it, so nothing is lost if the connection drops.
 */
class GatewayService : LifecycleService() {

    private lateinit var prefs: Prefs
    private lateinit var store: JobStore
    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .build()

    @Volatile private var socket: WebSocket? = null
    private var connectJob: Job? = null
    private val sendWake = MutableStateFlow(0L)

    override fun onCreate() {
        super.onCreate()
        prefs = Prefs(this)
        store = JobStore.get(this)
        ServiceCompat.startForeground(
            this, NOTIF_ID, notification("Connecting…"),
            // The remoteMessaging type exists from Android 14; older versions reject unknown type bits.
            if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING else 0,
        )
        _status.value = "Connecting…"

        connectJob = lifecycleScope.launch(Dispatchers.IO) { connectLoop() }
        lifecycleScope.launch(Dispatchers.IO) { senderLoop() }
        lifecycleScope.launch(Dispatchers.IO) {
            GatewayEvents.changes.collect { flushReports() }
        }
        lifecycleScope.launch(Dispatchers.IO) { housekeeping() }
    }

    override fun onDestroy() {
        socket?.close(1000, "service stopped")
        _status.value = "Stopped"
        super.onDestroy()
    }

    // ---- connection ---------------------------------------------------------

    private suspend fun connectLoop() {
        var backoff = 1_000L
        while (lifecycleScope.isActive) {
            val url = prefs.serverUrl
            val token = prefs.deviceToken
            if (url == null || token == null) {
                setStatus("Not paired")
                return
            }
            val closed = kotlinx.coroutines.CompletableDeferred<Unit>()
            val req = Request.Builder().url(wsUrl(url)).header("Authorization", "Bearer $token").build()
            socket = http.newWebSocket(req, object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    backoff = 1_000L
                    setStatus("Connected to $url")
                    lifecycleScope.launch(Dispatchers.IO) {
                        flushReports()
                        sendHeartbeat()
                        sendVerificationIfPending()
                    }
                }

                override fun onMessage(webSocket: WebSocket, text: String) {
                    runCatching { handle(JSONObject(text)) }.onFailure { Log.w(TAG, "bad frame: $text", it) }
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    if (response?.code == 401) setStatus("Server rejected this phone. Pair it again.")
                    else setStatus("Offline: ${t.message ?: "connection failed"}. Retrying…")
                    closed.complete(Unit)
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    setStatus("Disconnected. Retrying…")
                    closed.complete(Unit)
                }
            })
            closed.await()
            socket = null
            delay(backoff)
            backoff = min(backoff * 2, 60_000L)
        }
    }

    private fun handle(f: JSONObject) {
        when (f.optString("type")) {
            "hello" -> {
                val st = f.optString("device_status")
                if (st == "pending_verification") setStatus("Connected. Verifying your number…")
                else if (st == "active") setStatus("Connected and ready")
            }
            "send" -> {
                val id = f.getString("id")
                // Idempotency hop 2: a re-pushed job is stored once; we just re-report its state.
                if (store.insertJob(id, f.getString("to"), f.getString("body"))) {
                    sendWake.value = System.nanoTime()
                }
                store.job(id)?.let { report(it.id, it.state, it.error) }
            }
            "status_ack" -> store.markReported(f.getString("id"), f.getString("status"))
            "inbound_ack" -> store.ackInbound(f.getString("dedup"))
        }
    }

    private fun send(frame: JSONObject): Boolean = socket?.send(frame.toString()) ?: false

    private fun report(id: String, state: String, error: String?) {
        send(JSONObject().put("type", "status").put("id", id).put("status", state).apply { if (error != null) put("error", error) })
    }

    private fun flushReports() {
        if (socket == null) return
        store.unreported().forEach { report(it.id, it.state, it.error) }
        store.unackedInbound().forEach {
            send(
                JSONObject().put("type", "inbound").put("dedup", it.dedup).put("from", it.from)
                    .put("body", it.body).put("received_at", it.receivedAt)
            )
        }
    }

    private fun sendHeartbeat() {
        val bm = getSystemService(BatteryManager::class.java)
        val hb = JSONObject()
            .put("battery", bm?.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY) ?: -1)
            .put("app_version", BuildConfig.VERSION_NAME)
        send(JSONObject().put("type", "heartbeat").put("heartbeat", hb))
    }

    /** After pairing, text the code to our own number to prove we own it. */
    private fun sendVerificationIfPending() {
        val msg = prefs.pendingVerifyMessage ?: return
        val number = prefs.phoneNumber ?: return
        runCatching {
            SmsSender.send(this, prefs.subscriptionId, "verify", number, msg)
            prefs.pendingVerifyMessage = null
            setStatus("Verification SMS sent to $number…")
        }.onFailure { setStatus("Could not send the verification SMS: ${it.message}") }
    }

    // ---- sending --------------------------------------------------------------

    private suspend fun senderLoop() {
        while (lifecycleScope.isActive) {
            val job = store.nextToSend()
            if (job == null) {
                // Wait for a new job or poll every 5 s.
                val seen = sendWake.value
                var waited = 0
                while (sendWake.value == seen && waited < 50) { delay(100); waited++ }
                continue
            }
            val parts = runCatching { SmsSender.partCount(this, prefs.subscriptionId, job.body) }.getOrDefault(1)
            // Persist "sending" BEFORE calling SmsManager (idempotency hop 3).
            if (!store.markSending(job.id, parts)) continue
            report(job.id, JobState.SENDING, null)
            runCatching { SmsSender.send(this, prefs.subscriptionId, job.id, job.to, job.body) }
                .onFailure { store.advance(job.id, JobState.FAILED, it.message ?: "SmsManager error") }
            flushReports()
            delay(SEND_SPACING_MS) // be gentle with the carrier
        }
    }

    private suspend fun housekeeping() {
        while (lifecycleScope.isActive) {
            delay(60_000)
            if (store.expireSending(SENDING_TIMEOUT_MS) > 0) flushReports()
            sendHeartbeat()
            store.prune()
        }
    }

    // ---- status/notification ----------------------------------------------------

    private fun setStatus(s: String) {
        _status.value = s
        getSystemService(NotificationManager::class.java).notify(NOTIF_ID, notification(s))
    }

    private fun notification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        if (nm.getNotificationChannel(CHANNEL) == null) {
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "Gateway status", NotificationManager.IMPORTANCE_LOW))
        }
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_gateway)
            .setContentTitle("smsmcp Gateway")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .build()
    }

    companion object {
        private const val TAG = "smsmcp"
        private const val CHANNEL = "gateway"
        private const val NOTIF_ID = 1
        private const val SEND_SPACING_MS = 1_500L
        private const val SENDING_TIMEOUT_MS = 3 * 60_000L

        private val _status = MutableStateFlow("Stopped")
        val status: StateFlow<String> = _status

        fun start(context: Context) {
            ContextCompat.startForegroundService(context, Intent(context, GatewayService::class.java))
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, GatewayService::class.java))
        }
    }
}
