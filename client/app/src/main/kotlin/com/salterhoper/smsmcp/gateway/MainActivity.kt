package com.salterhoper.smsmcp.gateway

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.app.Application
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

class GatewayApp : Application()

/**
 * Setup screen: scan the pairing QR, pick the SIM/number, pair, and start the
 * gateway. Built in code (no XML layouts) to keep the scaffold small; replace
 * with Compose when the UI grows.
 */
class MainActivity : ComponentActivity() {

    private lateinit var prefs: Prefs
    private lateinit var statusView: TextView
    private lateinit var linkInput: EditText
    private lateinit var simSpinner: Spinner
    private lateinit var numberInput: EditText
    private lateinit var nameInput: EditText
    private lateinit var pairButton: Button
    private lateinit var toggleButton: Button
    private var sims: List<SimInfo> = emptyList()

    private val permissions = buildList {
        add(Manifest.permission.SEND_SMS)
        add(Manifest.permission.RECEIVE_SMS)
        add(Manifest.permission.READ_PHONE_STATE)
        add(Manifest.permission.READ_PHONE_NUMBERS)
        if (Build.VERSION.SDK_INT >= 33) add(Manifest.permission.POST_NOTIFICATIONS)
    }.toTypedArray()

    private val askPermissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        loadSims()
    }

    private val scan = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { linkInput.setText(it) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        setContentView(buildUi())
        intent?.data?.let { linkInput.setText(it.toString()) }

        lifecycleScope.launch {
            GatewayService.status.collect { s -> statusView.text = statusLine(s) }
        }
        if (permissions.any { ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED }) {
            askPermissions.launch(permissions)
        } else {
            loadSims()
        }
        refresh()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.data?.let { linkInput.setText(it.toString()) }
    }

    private fun statusLine(s: String) =
        if (prefs.isPaired) "Paired as ${prefs.phoneNumber}\n$s" else "Not paired yet. Run `smsmcp pair` on your server and scan the code."

    private fun refresh() {
        statusView.text = statusLine(GatewayService.status.value)
        pairButton.text = if (prefs.isPaired) "Pair again (replaces this pairing)" else "Pair this phone"
        toggleButton.visibility = if (prefs.isPaired) View.VISIBLE else View.GONE
        toggleButton.text = if (prefs.gatewayEnabled) "Stop gateway" else "Start gateway"
    }

    private fun loadSims() {
        sims = Sims.list(this)
        val labels = if (sims.isEmpty()) listOf("Default SIM") else sims.map { it.label }
        simSpinner.adapter = ArrayAdapter(this, android.R.layout.simple_spinner_dropdown_item, labels)
        simSpinner.onItemSelectedListener = object : android.widget.AdapterView.OnItemSelectedListener {
            override fun onItemSelected(p: android.widget.AdapterView<*>?, v: View?, pos: Int, id: Long) {
                sims.getOrNull(pos)?.number?.let { if (numberInput.text.isBlank()) numberInput.setText(it) }
            }
            override fun onNothingSelected(p: android.widget.AdapterView<*>?) {}
        }
    }

    private fun pair() {
        val link = PairingLink.parse(linkInput.text.toString())
        if (link == null) {
            statusView.text = "That isn't a pairing link. It should start with smsmcp://pair"
            return
        }
        val number = numberInput.text.toString().trim()
        if (number.length < 5) {
            statusView.text = "Enter the phone number of the SIM you picked, e.g. +255712345678"
            return
        }
        val subId = sims.getOrNull(simSpinner.selectedItemPosition)?.subscriptionId ?: -1
        val name = nameInput.text.toString().ifBlank { Build.MODEL }
        pairButton.isEnabled = false
        statusView.text = "Pairing with ${link.serverUrl}…"

        lifecycleScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    postJson(
                        "${link.serverUrl}/v1/pair",
                        JSONObject().put("token", link.token).put("device_name", name)
                            .put("phone_number", number).put("sim_subscription_id", subId),
                    )
                }
            }
            pairButton.isEnabled = true
            result.onSuccess { r ->
                GatewayService.stop(this@MainActivity)
                prefs.serverUrl = link.serverUrl
                prefs.deviceId = r.getString("device_id")
                prefs.deviceToken = r.getString("device_token")
                prefs.phoneNumber = r.getString("phone_number")
                prefs.subscriptionId = subId
                prefs.pendingVerifyMessage = r.getString("verify_message")
                prefs.gatewayEnabled = true
                linkInput.setText("")
                GatewayService.start(this@MainActivity)
                refresh()
            }.onFailure { statusView.text = "Pairing failed: ${it.message}" }
        }
    }

    private fun postJson(url: String, body: JSONObject): JSONObject {
        val conn = URL(url).openConnection() as HttpURLConnection
        try {
            conn.requestMethod = "POST"
            conn.connectTimeout = 10_000
            conn.readTimeout = 15_000
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write(body.toString().toByteArray()) }
            val code = conn.responseCode
            val text = (if (code in 200..299) conn.inputStream else conn.errorStream)?.bufferedReader()?.readText().orEmpty()
            if (code !in 200..299) {
                val msg = runCatching { JSONObject(text).getString("error") }.getOrDefault("HTTP $code")
                throw IllegalStateException(msg)
            }
            return JSONObject(text)
        } finally {
            conn.disconnect()
        }
    }

    private fun toggleGateway() {
        prefs.gatewayEnabled = !prefs.gatewayEnabled
        if (prefs.gatewayEnabled) GatewayService.start(this) else GatewayService.stop(this)
        refresh()
    }

    @SuppressLint("BatteryLife")
    private fun askBatteryExemption() {
        val pm = getSystemService(PowerManager::class.java)
        if (pm.isIgnoringBatteryOptimizations(packageName)) {
            statusView.text = "Battery optimisation is already off for this app."
            return
        }
        startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
    }

    // ---- layout -----------------------------------------------------------------

    private fun buildUi(): View {
        val pad = (16 * resources.displayMetrics.density).toInt()
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }
        fun label(text: String) = TextView(this).apply { this.text = text; setPadding(0, pad, 0, pad / 4) }

        col.addView(TextView(this).apply { text = "smsmcp Gateway"; textSize = 24f })
        statusView = TextView(this).apply { textSize = 15f; setPadding(0, pad / 2, 0, pad / 2) }
        col.addView(statusView)

        col.addView(label("1. Pairing link"))
        col.addView(Button(this).apply {
            text = "Scan pairing QR"
            setOnClickListener {
                scan.launch(ScanOptions().setPrompt("Scan the code from `smsmcp pair`").setBeepEnabled(false).setOrientationLocked(false))
            }
        })
        linkInput = EditText(this).apply {
            hint = "or paste smsmcp://pair?…"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
        }
        col.addView(linkInput)

        col.addView(label("2. SIM that sends and receives for agents"))
        simSpinner = Spinner(this)
        col.addView(simSpinner)
        numberInput = EditText(this).apply { hint = "Phone number of that SIM, e.g. +255712345678"; inputType = InputType.TYPE_CLASS_PHONE }
        col.addView(numberInput)

        col.addView(label("3. Name for this phone"))
        nameInput = EditText(this).apply { setText(Build.MODEL); inputType = InputType.TYPE_CLASS_TEXT }
        col.addView(nameInput)

        pairButton = Button(this).apply { setOnClickListener { pair() } }
        col.addView(pairButton)
        toggleButton = Button(this).apply { setOnClickListener { toggleGateway() } }
        col.addView(toggleButton)
        col.addView(Button(this).apply {
            text = "Keep running in background (battery settings)"
            setOnClickListener { askBatteryExemption() }
        })
        col.addView(TextView(this).apply {
            text = "Android limits apps to about 30 SMS per 30 minutes. To raise it, run on your computer:\n" +
                "adb shell settings put global sms_outgoing_check_max_count 1000"
            textSize = 12f
            gravity = Gravity.START
            setTextIsSelectable(true)
            setPadding(0, pad, 0, 0)
        })
        return ScrollView(this).apply { addView(col) }
    }
}
