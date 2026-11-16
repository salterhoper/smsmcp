package com.salterhoper.smsmcp.gateway

import android.Manifest
import android.annotation.SuppressLint
import android.app.Application
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.ColorStateList
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.View
import android.widget.ArrayAdapter
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.net.toUri
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import com.google.android.material.snackbar.Snackbar
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import com.salterhoper.smsmcp.gateway.databinding.ActivityMainBinding
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

class GatewayApp : Application()

/**
 * Setup screen: a status card, then pairing (scan the QR, pick the SIM and
 * number, pair), then reliability settings. Layout: res/layout/activity_main.xml.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var prefs: Prefs
    private lateinit var ui: ActivityMainBinding
    private var sims: List<SimInfo> = emptyList()
    private var simIndex = 0

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
        result.contents?.let { ui.linkInput.setText(it) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        ui = ActivityMainBinding.inflate(layoutInflater)
        setContentView(ui.root)
        applyInsets()
        bindActions()
        ui.nameInput.setText(Build.MODEL)
        intent?.data?.let { ui.linkInput.setText(it.toString()) }

        lifecycleScope.launch {
            GatewayService.status.collect { renderStatus(it) }
        }
        if (permissions.any { ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED }) {
            askPermissions.launch(permissions)
        } else {
            loadSims()
        }
        refresh()
    }

    override fun onResume() {
        super.onResume()
        renderBattery() // the user may be back from the battery settings screen
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.data?.let { ui.linkInput.setText(it.toString()) }
    }

    private fun applyInsets() {
        ViewCompat.setOnApplyWindowInsetsListener(ui.root) { _, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val ime = insets.getInsets(WindowInsetsCompat.Type.ime())
            ui.root.updatePadding(left = bars.left, top = bars.top, right = bars.right)
            ui.scroll.updatePadding(bottom = maxOf(bars.bottom, ime.bottom))
            WindowInsetsCompat.CONSUMED
        }
    }

    private fun bindActions() {
        ui.scanButton.setOnClickListener {
            scan.launch(ScanOptions().setPrompt(getString(R.string.scan_prompt)).setBeepEnabled(false).setOrientationLocked(false))
        }
        ui.linkInput.addTextChangedListener(clearError { ui.linkLayout.error = null })
        ui.numberInput.addTextChangedListener(clearError { ui.numberLayout.error = null })
        ui.simInput.setOnItemClickListener { _, _, pos, _ ->
            simIndex = pos
            sims.getOrNull(pos)?.number?.let { if (ui.numberInput.text.isNullOrBlank()) ui.numberInput.setText(it) }
        }
        ui.pairButton.setOnClickListener { pair() }
        ui.toggleButton.setOnClickListener { toggleGateway() }
        ui.batteryButton.setOnClickListener { askBatteryExemption() }
        ui.copyButton.setOnClickListener { copyLimitCommand() }
    }

    private fun clearError(clear: () -> Unit) = object : android.text.TextWatcher {
        override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
        override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = clear()
        override fun afterTextChanged(s: android.text.Editable?) {}
    }

    // ---- rendering --------------------------------------------------------------

    private fun refresh() {
        renderStatus(GatewayService.status.value)
        val paired = prefs.isPaired
        ui.pairTitle.setText(if (paired) R.string.section_repair else R.string.section_pair)
        ui.pairHint.visibility = if (paired) View.VISIBLE else View.GONE
        ui.pairButton.setText(if (paired) R.string.section_repair else R.string.pair)
        ui.toggleButton.visibility = if (paired) View.VISIBLE else View.GONE
        ui.toggleButton.setText(if (prefs.gatewayEnabled) R.string.stop_gateway else R.string.start_gateway)
        renderBattery()
    }

    private fun renderStatus(s: GatewayStatus) {
        val health: Health
        val title: String
        val detail: String
        when {
            !prefs.isPaired -> {
                health = Health.OFF; title = getString(R.string.status_not_paired); detail = getString(R.string.status_not_paired_detail)
            }
            !prefs.gatewayEnabled -> {
                health = Health.OFF; title = getString(R.string.status_off); detail = getString(R.string.status_off_detail)
            }
            // Enabled, but the service hasn't reported yet.
            s.health == Health.OFF -> {
                health = Health.WORKING; title = getString(R.string.status_working); detail = s.text
            }
            else -> {
                health = s.health
                title = getString(
                    when (health) {
                        Health.READY -> R.string.status_ready
                        Health.ERROR -> R.string.status_error
                        else -> R.string.status_working
                    },
                )
                detail = s.text
            }
        }
        val (bg, fg, icon) = when (health) {
            Health.READY -> Triple(R.color.health_ready_bg, R.color.health_ready_fg, R.drawable.ic_cloud_done)
            Health.WORKING -> Triple(R.color.health_working_bg, R.color.health_working_fg, R.drawable.ic_sync)
            Health.ERROR -> Triple(R.color.health_error_bg, R.color.health_error_fg, R.drawable.ic_error)
            Health.OFF -> Triple(
                R.color.health_off_bg, R.color.health_off_fg,
                if (prefs.isPaired) R.drawable.ic_cloud_off else R.drawable.ic_link_off,
            )
        }
        val fgColor = getColor(fg)
        ui.statusCard.setCardBackgroundColor(getColor(bg))
        ui.statusIcon.setImageResource(icon)
        ui.statusIcon.imageTintList = ColorStateList.valueOf(fgColor)
        ui.statusTitle.text = title
        ui.statusTitle.setTextColor(fgColor)
        ui.statusDetail.text = detail
        ui.statusDetail.setTextColor(fgColor)
        ui.statusNumber.setTextColor(fgColor)
        ui.statusNumber.visibility = if (prefs.isPaired) View.VISIBLE else View.GONE
        ui.statusNumber.text = getString(R.string.paired_as, prefs.phoneNumber.orEmpty())
    }

    private fun renderBattery() {
        val exempt = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)
        ui.batteryStatus.setText(if (exempt) R.string.battery_off else R.string.battery_on)
        ui.batteryButton.visibility = if (exempt) View.GONE else View.VISIBLE
    }

    private fun loadSims() {
        sims = Sims.list(this)
        val labels = if (sims.isEmpty()) listOf(getString(R.string.default_sim)) else sims.map { it.label }
        ui.simInput.setAdapter(ArrayAdapter(this, android.R.layout.simple_list_item_1, labels))
        simIndex = simIndex.coerceIn(0, labels.lastIndex)
        ui.simInput.setText(labels[simIndex], false)
        sims.getOrNull(simIndex)?.number?.let { if (ui.numberInput.text.isNullOrBlank()) ui.numberInput.setText(it) }
    }

    // ---- actions ----------------------------------------------------------------

    private fun pair() {
        val link = PairingLink.parse(ui.linkInput.text?.toString().orEmpty())
        if (link == null) {
            ui.linkLayout.error = getString(R.string.error_link)
            return
        }
        val number = ui.numberInput.text?.toString().orEmpty().trim()
        if (number.length < 5) {
            ui.numberLayout.error = getString(R.string.error_number)
            return
        }
        val subId = sims.getOrNull(simIndex)?.subscriptionId ?: -1
        val name = ui.nameInput.text?.toString().orEmpty().ifBlank { Build.MODEL }
        ui.pairButton.isEnabled = false
        ui.pairButton.setText(R.string.pairing)

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
            ui.pairButton.isEnabled = true
            result.onSuccess { r ->
                GatewayService.stop(this@MainActivity)
                prefs.serverUrl = link.serverUrl
                prefs.deviceId = r.getString("device_id")
                prefs.deviceToken = r.getString("device_token")
                prefs.phoneNumber = r.getString("phone_number")
                prefs.subscriptionId = subId
                prefs.pendingVerifyMessage = r.getString("verify_message")
                prefs.gatewayEnabled = true
                ui.linkInput.setText("")
                GatewayService.start(this@MainActivity)
                ui.scroll.smoothScrollTo(0, 0)
            }.onFailure {
                Snackbar.make(ui.root, getString(R.string.pair_failed, it.message), Snackbar.LENGTH_LONG).show()
            }
            refresh()
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
        startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, "package:$packageName".toUri()))
    }

    private fun copyLimitCommand() {
        getSystemService(ClipboardManager::class.java)
            .setPrimaryClip(ClipData.newPlainText("adb command", getString(R.string.limit_command)))
        // Android 13+ shows its own clipboard confirmation.
        if (Build.VERSION.SDK_INT < 33) Snackbar.make(ui.root, R.string.copied, Snackbar.LENGTH_SHORT).show()
    }
}
