package net.hotelpos.opsview

import android.annotation.SuppressLint
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Network
import android.net.Uri
import android.os.Bundle
import android.os.SystemClock
import android.view.WindowManager
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.EditText
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat

class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var prefs: Prefs
    private var lastBackAt = 0L
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    private var sawNetworkLoss = false

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        prefs = Prefs(this)

        webView = WebView(this)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true                 // page uses localStorage
        webView.settings.mediaPlaybackRequiresUserGesture = false // muted autoplay wall <video>
        android.webkit.CookieManager.getInstance().setAcceptCookie(true)
        android.webkit.CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true)
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val target = request.url.toString()
                if (UrlPolicy.isInApp(prefs.startUrl(), target)) return false // load in WebView
                startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(target)))   // off-origin → browser
                return true
            }
        }
        setContentView(webView)

        webView.setOnLongClickListener {
            showServerDialog()
            true
        }

        webView.loadUrl(prefs.startUrl())
        enterImmersive()

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                // If the enlarge overlay (#wallbig.show) is open, close it; otherwise
                // require a second back press within 2s to exit (avoids accidental quit).
                webView.evaluateJavascript(
                    "(function(){var b=document.getElementById('wallbig');" +
                        "if(b&&b.classList.contains('show')){var c=b.querySelector('.wbig-close');" +
                        "if(c){c.click();return 'closed';}}return 'none';})();"
                ) { result ->
                    if (result != null && result.contains("closed")) return@evaluateJavascript
                    val now = SystemClock.elapsedRealtime()
                    if (now - lastBackAt < 2000) {
                        finish()
                    } else {
                        lastBackAt = now
                        Toast.makeText(this@MainActivity, R.string.exit_hint, Toast.LENGTH_SHORT).show()
                    }
                }
            }
        })
    }

    private fun enterImmersive() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val c = WindowInsetsControllerCompat(window, window.decorView)
        c.hide(WindowInsetsCompat.Type.systemBars())
        c.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) enterImmersive()
    }

    override fun onResume() {
        super.onResume()
        val cm = getSystemService(ConnectivityManager::class.java)
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onLost(network: Network) { sawNetworkLoss = true }
            override fun onAvailable(network: Network) {
                if (sawNetworkLoss) {
                    sawNetworkLoss = false
                    runOnUiThread { webView.reload() }
                }
            }
        }
        netCallback = cb
        cm.registerDefaultNetworkCallback(cb)
    }

    override fun onPause() {
        super.onPause()
        android.webkit.CookieManager.getInstance().flush()
        netCallback?.let { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(it) }
        netCallback = null
    }

    private fun showServerDialog() {
        val input = EditText(this).apply {
            setText(prefs.startUrl())
            setSelection(text.length)
        }
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_title)
            .setView(input)
            .setPositiveButton(R.string.settings_save) { _, _ ->
                val v = input.text.toString().trim()
                if (v.startsWith("https://")) {
                    prefs.setStartUrl(v)
                    webView.loadUrl(v)
                }
            }
            .setNegativeButton(R.string.settings_cancel, null)
            .show()
    }
}
