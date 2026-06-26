# OpsView Android Wall Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a minimal Android APK that wraps the relay's existing `/dashboard/wall` page in a WebView so a venue owner can watch their live CCTV wall on a phone from anywhere.

**Architecture:** A thin single-Activity Kotlin app hosts a WebView pointing at `https://ops.hotelpos.net/dashboard/wall` (reachable via the existing Cloudflare Tunnel). The native layer adds only what a browser tab can't: keep-screen-on, immersive fullscreen, back-button semantics, persistent cookies (auto-login), and network-change reconnect. Two small relay changes let the phone land on the wall after login and make the page finger-friendly.

**Tech Stack:** Kotlin, AndroidX, Android Gradle Plugin 8.5.2, Gradle 8.7, Kotlin 1.9.24, `compileSdk`/`targetSdk` 34, `minSdk` 26. Relay side is Go 1.25 + vanilla JS/CSS assets (existing).

## Global Constraints

- Android application id: `net.hotelpos.opsview` (exact).
- `minSdk 26`, `targetSdk 34`, `compileSdk 34`; Kotlin + AndroidX; single Activity.
- Default start URL (exact): `https://ops.hotelpos.net/dashboard/wall`. HTTPS only (`usesCleartextTraffic=false`); start URL is user-overridable via SharedPreferences.
- Distribution: signed APK attached to the GitHub Release (sideload). No Play Store.
- Dashboard facts the code must match: cookie name `opsview_dash`, session TTL 12h, cookie `Path=/dashboard`, login `POST /dashboard/api/login`, wall page route `/dashboard/wall`, layout API `/dashboard/api/wall-layout?agent=`.
- **Prerequisite for all Android tasks (Tasks 3-7):** an Android SDK must be installed and `ANDROID_HOME` (or `android/local.properties` with `sdk.dir=...`) set; install a system Gradle once (`brew install gradle`) to generate the wrapper in Task 3. Without the SDK, `./gradlew` cannot configure the project.
- Push policy: this is new Android + relay work. Commit each task; do NOT push tags until the user explicitly says so (the user batches release cuts).

---

### Task 1: Relay — `?next=` redirect from the wall page

Unauthed requests to `/dashboard/wall` currently 302 to `/dashboard`, where the login SPA lands on the dashboard index, not the wall. Make the redirect carry a `next` hint so the phone returns to the wall after login.

**Files:**
- Modify: `relay/dashboard.go` (function `HandleWallPage`, currently around lines 463-481)
- Test: `relay/dashboard_wall_test.go` (create)

**Interfaces:**
- Consumes: `(*Hub).authedDashboard(r) bool`, `dashboardCookieName`, `signSession(token string, exp time.Time) string`, `(*Hub).effectiveDashToken() string`, `NewHub(Config)`, `cfgWithDash(string) Config` (test helper in `relay/dashboard_test.go`).
- Produces: unauthed `GET /dashboard/wall` → `302` with `Location: /dashboard?next=%2Fdashboard%2Fwall`; authed → `200` serving `wall.html`.

- [ ] **Step 1: Write the failing test**

Create `relay/dashboard_wall_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWallPageRedirectsUnauthedWithNext(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	rec := httptest.NewRecorder()
	h.HandleWallPage(rec, httptest.NewRequest("GET", "/dashboard/wall", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("unauthed: want 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard?next=%2Fdashboard%2Fwall" {
		t.Fatalf("unauthed Location = %q, want /dashboard?next=%%2Fdashboard%%2Fwall", loc)
	}
}

func TestWallPageServesWhenAuthed(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	req := httptest.NewRequest("GET", "/dashboard/wall", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	h.HandleWallPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed: want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("authed Content-Type = %q", ct)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && go test ./... -run TestWallPage -v`
Expected: `TestWallPageRedirectsUnauthedWithNext` FAILS — current `Location` is `/dashboard` (no `?next=`).

- [ ] **Step 3: Update `HandleWallPage` to add the `next` param**

In `relay/dashboard.go`, change the redirect line inside `HandleWallPage` from:

```go
	if !h.authedDashboard(r) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
```

to:

```go
	if !h.authedDashboard(r) {
		// Carry a same-origin next hint so the login SPA returns the caller to the
		// wall after authenticating (used by the Android app and desktop deep links).
		http.Redirect(w, r, "/dashboard?next=%2Fdashboard%2Fwall", http.StatusFound)
		return
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd relay && go test ./... -run TestWallPage -v`
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/dashboard.go relay/dashboard_wall_test.go
git commit -m "feat(relay): wall page redirects unauthed users with ?next=/dashboard/wall"
```

---

### Task 2: Relay — login SPA honors `next` + mobile CSS polish

Make the login SPA send the user to a safe same-origin `next` after authenticating, and make the wall/login finger-friendly on a phone. No JS unit harness exists for `dashboard_assets`, so this task verifies via build + manual steps (the existing assets like `wall.js` are likewise covered by manual/integration testing).

**Files:**
- Modify: `relay/dashboard_assets/app.js` (login success in `attempt()` ~line 206; initial authed check ~line 228)
- Modify: `relay/dashboard_assets/style.css` (append a mobile block)

**Interfaces:**
- Consumes: `location.search` `next` param set by Task 1's redirect.
- Produces: after a successful login (or when already authed on load), if `next` is a safe same-origin `/dashboard…` path, the SPA navigates there via `location.href`.

- [ ] **Step 1: Add a safe-next helper and use it on login success**

In `relay/dashboard_assets/app.js`, add this helper near the top of the file (after the existing `var`/`function` declarations, before `function attempt()`):

```javascript
// safeNext returns the ?next= path only when it is a same-origin /dashboard
// path (blocks open-redirects like //evil.com or https://evil.com). Else null.
function safeNext(){
  var n=new URLSearchParams(location.search).get('next');
  if(!n) return null;
  if(n.charAt(0)!=='/' || n.charAt(1)==='/') return null; // must be path, not //host
  if(n!=='/dashboard' && n.indexOf('/dashboard/')!==0 && n.indexOf('/dashboard?')!==0) return null;
  return n;
}
```

Then in `attempt()`, change the success branch from:

```javascript
      if(r.status===200){ loginMsg.textContent=''; loginMsg.className='login-msg'; fails=0; showApp(); }
```

to:

```javascript
      if(r.status===200){ loginMsg.textContent=''; loginMsg.className='login-msg'; fails=0; var nx=safeNext(); if(nx){ location.href=nx; return; } showApp(); }
```

- [ ] **Step 2: Honor `next` when already authed on load**

In `relay/dashboard_assets/app.js`, change the initial state check from:

```javascript
fetch('/dashboard/api/state').then(function(r){
  if(r.status===200){ showApp(); } else { showLogin(); }
}).catch(showLogin);
```

to:

```javascript
fetch('/dashboard/api/state').then(function(r){
  if(r.status===200){ var nx=safeNext(); if(nx){ location.href=nx; return; } showApp(); } else { showLogin(); }
}).catch(showLogin);
```

- [ ] **Step 3: Append mobile CSS polish**

Append to the end of `relay/dashboard_assets/style.css`:

```css
/* ===== Mobile (phone) polish for the Android wall app and small screens ===== */
@media (max-width: 700px) {
  .login-card, .loginCard { width: 92vw; max-width: 360px; }
  #pw, .input { font-size: 16px; min-height: 44px; } /* 16px avoids iOS/Android zoom; 44px tap target */
  .wcell { min-height: 44px; }
  .wcell .wname { font-size: 13px; }
  .wbig-close { min-width: 48px; min-height: 48px; font-size: 22px; }
  .wbig-bar { padding: 8px 12px; }
}
```

- [ ] **Step 4: Build the relay to confirm embedded assets still compile in**

Run: `cd relay && go build ./...`
Expected: builds with no error (assets are `go:embed`-ed; a build proves they are present and valid to embed).

- [ ] **Step 5: Manual verification (record result in the task report)**

Procedure (against a relay with `RELAY_WALL=1` and at least one online agent, or the production tunnel):
1. In a browser, open `https://<relay>/dashboard/wall` while logged out → you are redirected to `…/dashboard?next=%2Fdashboard%2Fwall` and see the login form.
2. Enter the password → on success the page navigates to `/dashboard/wall` (not the dashboard index).
3. Open `https://<relay>/dashboard?next=//evil.com` and log in → you land on `showApp()` (the dashboard), NOT an external site (open-redirect blocked).
4. Narrow the browser to phone width (~390px) → login input and wall ✕ button are comfortably tappable.

Expected: steps 1-2 land on the wall, step 3 stays on-origin, step 4 looks finger-friendly.

- [ ] **Step 6: Commit**

```bash
git add relay/dashboard_assets/app.js relay/dashboard_assets/style.css
git commit -m "feat(relay): login SPA honors safe ?next= and mobile CSS polish for wall"
```

---

### Task 3: Android — Gradle scaffold + manifest + minimal WebView Activity

Stand up the `android/` module with a single Activity that loads the start URL in a WebView. This task's deliverable is a debug APK that builds and loads the wall page; richer behavior comes in Tasks 4-6.

**Files:**
- Create: `android/settings.gradle.kts`
- Create: `android/build.gradle.kts`
- Create: `android/gradle.properties`
- Create: `android/app/build.gradle.kts`
- Create: `android/app/src/main/AndroidManifest.xml`
- Create: `android/app/src/main/java/net/hotelpos/opsview/Prefs.kt`
- Create: `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`
- Create: `android/app/src/main/res/values/strings.xml`
- Create: `android/.gitignore`

**Interfaces:**
- Produces: `Prefs(ctx).startUrl(): String` and `Prefs(ctx).setStartUrl(v: String)`; `MainActivity` hosting a `WebView` field reachable by later tasks.

- [ ] **Step 1: Create `android/settings.gradle.kts`**

```kotlin
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}
rootProject.name = "OpsViewAndroid"
include(":app")
```

- [ ] **Step 2: Create root `android/build.gradle.kts`**

```kotlin
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}
```

- [ ] **Step 3: Create `android/gradle.properties`**

```properties
org.gradle.jvmargs=-Xmx2048m
android.useAndroidX=true
kotlin.code.style=official
```

- [ ] **Step 4: Create `android/app/build.gradle.kts`**

```kotlin
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "net.hotelpos.opsview"
    compileSdk = 34

    defaultConfig {
        applicationId = "net.hotelpos.opsview"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.1")
    implementation("androidx.webkit:webkit:1.11.0")
    testImplementation("junit:junit:4.13.2")
}
```

- [ ] **Step 5: Create `android/app/src/main/res/values/strings.xml`**

```xml
<?xml version="1.0" encoding="utf-8"?>
<resources>
    <string name="app_name">OpsView</string>
    <string name="settings_title">서버 주소</string>
    <string name="settings_save">저장</string>
    <string name="settings_cancel">취소</string>
    <string name="exit_hint">한 번 더 누르면 종료</string>
</resources>
```

- [ ] **Step 6: Create `android/app/src/main/AndroidManifest.xml`**

```xml
<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android">

    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />

    <application
        android:allowBackup="false"
        android:label="@string/app_name"
        android:supportsRtl="true"
        android:usesCleartextTraffic="false"
        android:theme="@style/Theme.AppCompat.NoActionBar">
        <activity
            android:name=".MainActivity"
            android:exported="true"
            android:configChanges="orientation|screenSize|keyboardHidden|smallestScreenSize|screenLayout"
            android:screenOrientation="fullSensor">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>
        </activity>
    </application>
</manifest>
```

- [ ] **Step 7: Create `android/app/src/main/java/net/hotelpos/opsview/Prefs.kt`**

```kotlin
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
```

- [ ] **Step 8: Create a minimal `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`**

```kotlin
package net.hotelpos.opsview

import android.annotation.SuppressLint
import android.os.Bundle
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity

class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var prefs: Prefs

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)

        webView = WebView(this)
        webView.settings.javaScriptEnabled = true
        webView.webViewClient = WebViewClient()
        setContentView(webView)

        webView.loadUrl(prefs.startUrl())
    }
}
```

- [ ] **Step 9: Create `android/.gitignore`**

```gitignore
.gradle/
build/
local.properties
*.iml
.idea/
*.keystore
*.jks
```

- [ ] **Step 10: Generate the Gradle wrapper and build the debug APK**

Run (requires a system Gradle once — `brew install gradle` — and the Android SDK with `ANDROID_HOME` set or `android/local.properties` containing `sdk.dir=/path/to/sdk`):

```bash
cd android
gradle wrapper --gradle-version 8.7
./gradlew assembleDebug
```

Expected: `BUILD SUCCESSFUL`; APK at `android/app/build/outputs/apk/debug/app-debug.apk`. (The `gradle wrapper` step writes `gradlew`, `gradlew.bat`, and `gradle/wrapper/*` — commit them so CI and other devs use the pinned Gradle.)

- [ ] **Step 11: Commit**

```bash
git add android/
git commit -m "feat(android): scaffold WebView wall viewer app (loads start URL)"
```

---

### Task 4: Android — same-origin URL policy (pure, TDD) wired into the WebViewClient

Keep relay-origin navigation inside the WebView; bounce any off-origin link to the system browser. The decision is a pure function so it is JVM-unit-testable without a device.

**Files:**
- Create: `android/app/src/main/java/net/hotelpos/opsview/UrlPolicy.kt`
- Create: `android/app/src/test/java/net/hotelpos/opsview/UrlPolicyTest.kt`
- Modify: `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`

**Interfaces:**
- Consumes: `Prefs.startUrl()` as the base origin.
- Produces: `UrlPolicy.isInApp(baseUrl: String, target: String): Boolean` — true when `target`'s host equals `baseUrl`'s host (case-insensitive), false otherwise (including unparseable/`null` hosts).

- [ ] **Step 1: Write the failing test**

Create `android/app/src/test/java/net/hotelpos/opsview/UrlPolicyTest.kt`:

```kotlin
package net.hotelpos.opsview

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UrlPolicyTest {
    private val base = "https://ops.hotelpos.net/dashboard/wall"

    @Test fun sameHostIsInApp() {
        assertTrue(UrlPolicy.isInApp(base, "https://ops.hotelpos.net/dashboard"))
        assertTrue(UrlPolicy.isInApp(base, "https://ops.hotelpos.net/surv/ws/x/wall"))
    }

    @Test fun differentHostIsExternal() {
        assertFalse(UrlPolicy.isInApp(base, "https://evil.com/x"))
        assertFalse(UrlPolicy.isInApp(base, "https://ops.hotelpos.net.evil.com/x"))
    }

    @Test fun unparseableOrSchemeLinksAreExternal() {
        assertFalse(UrlPolicy.isInApp(base, "mailto:a@b.com"))
        assertFalse(UrlPolicy.isInApp(base, "not a url"))
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd android && ./gradlew :app:testDebugUnitTest --tests "net.hotelpos.opsview.UrlPolicyTest"`
Expected: FAIL — `UrlPolicy` does not exist (compile error / unresolved reference).

- [ ] **Step 3: Implement `UrlPolicy`**

Create `android/app/src/main/java/net/hotelpos/opsview/UrlPolicy.kt`:

```kotlin
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd android && ./gradlew :app:testDebugUnitTest --tests "net.hotelpos.opsview.UrlPolicyTest"`
Expected: PASS (3 tests).

- [ ] **Step 5: Wire the policy into `MainActivity`'s WebViewClient**

In `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`, replace the `webView.webViewClient = WebViewClient()` line with a custom client and add the imports. The full updated imports block and client:

Add imports at the top:

```kotlin
import android.content.Intent
import android.net.Uri
import android.webkit.WebResourceRequest
```

Replace `webView.webViewClient = WebViewClient()` with:

```kotlin
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val target = request.url.toString()
                if (UrlPolicy.isInApp(prefs.startUrl(), target)) return false // load in WebView
                startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(target)))   // off-origin → browser
                return true
            }
        }
```

- [ ] **Step 6: Build to confirm the wiring compiles**

Run: `cd android && ./gradlew assembleDebug`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 7: Commit**

```bash
git add android/app/src/main/java/net/hotelpos/opsview/UrlPolicy.kt android/app/src/test/java/net/hotelpos/opsview/UrlPolicyTest.kt android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt
git commit -m "feat(android): same-origin URL policy keeps relay nav in-app, links to browser"
```

---

### Task 5: Android — WebView config: persistent cookies, autoplay, keep-screen-on, immersive

Make the app behave like a kiosk-ish monitor: stay logged in, autoplay the muted wall video, keep the screen awake, and run edge-to-edge with system bars hidden.

**Files:**
- Modify: `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`

**Interfaces:**
- Consumes: existing `webView`, `prefs`.
- Produces: configured WebView (DOM storage, autoplay, persistent cookies flushed on pause); window with `FLAG_KEEP_SCREEN_ON` and immersive sticky bars.

- [ ] **Step 1: Configure WebView settings and cookies**

In `MainActivity.onCreate`, immediately after `webView.settings.javaScriptEnabled = true`, add:

```kotlin
        webView.settings.domStorageEnabled = true                 // page uses localStorage
        webView.settings.mediaPlaybackRequiresUserGesture = false // muted autoplay wall <video>
        android.webkit.CookieManager.getInstance().setAcceptCookie(true)
        android.webkit.CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true)
```

- [ ] **Step 2: Keep the screen on**

In `MainActivity.onCreate`, after `super.onCreate(...)`, add the import and flag.

Add import at top:

```kotlin
import android.view.WindowManager
```

Add in `onCreate` (before building the WebView):

```kotlin
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
```

- [ ] **Step 3: Immersive sticky fullscreen**

Add imports at top:

```kotlin
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
```

Add a helper method to `MainActivity`:

```kotlin
    private fun enterImmersive() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val c = WindowInsetsControllerCompat(window, window.decorView)
        c.hide(WindowInsetsCompat.Type.systemBars())
        c.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
    }
```

Call it at the end of `onCreate` (after `webView.loadUrl(...)`) and re-assert it on focus by overriding:

```kotlin
    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) enterImmersive()
    }
```

- [ ] **Step 4: Flush cookies on pause so login persists across restarts**

Add to `MainActivity`:

```kotlin
    override fun onPause() {
        super.onPause()
        android.webkit.CookieManager.getInstance().flush()
    }
```

- [ ] **Step 5: Build to confirm it compiles**

Run: `cd android && ./gradlew assembleDebug`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 6: Manual verification (record in task report; needs a device/emulator + reachable relay)**

1. Launch the app → wall page loads; after one login the muted wall video autoplays.
2. Confirm status/nav bars are hidden (immersive); swipe reveals them transiently.
3. Leave the app open 2 minutes → screen does not dim/sleep.
4. Kill and relaunch the app → it opens straight to the wall (still logged in), no password prompt (within the 12h cookie TTL).

Expected: all four hold.

- [ ] **Step 7: Commit**

```bash
git add android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt
git commit -m "feat(android): persistent cookies, autoplay, keep-screen-on, immersive fullscreen"
```

---

### Task 6: Android — back-button semantics, network reconnect, start-URL settings dialog

Add the three remaining native behaviors: back closes the enlarge overlay (or double-back exits), a network-regain triggers a reload, and a long-press opens a dialog to change the server URL.

**Files:**
- Modify: `android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt`

**Interfaces:**
- Consumes: `webView`, `prefs`, `Prefs.setStartUrl`, strings `settings_title`/`settings_save`/`settings_cancel`/`exit_hint`.
- Produces: an `OnBackPressedCallback` registered on the dispatcher; a `ConnectivityManager.NetworkCallback` registered in `onResume` and unregistered in `onPause`; a long-press settings dialog.

- [ ] **Step 1: Back-button — close overlay if open, else double-back to exit**

Add imports:

```kotlin
import android.os.SystemClock
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
```

Add a field to `MainActivity`:

```kotlin
    private var lastBackAt = 0L
```

Register the callback at the end of `onCreate`:

```kotlin
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
```

- [ ] **Step 2: Network reconnect — reload when connectivity returns after a loss**

Add imports:

```kotlin
import android.net.ConnectivityManager
import android.net.Network
```

Add fields:

```kotlin
    private var netCallback: ConnectivityManager.NetworkCallback? = null
    private var sawNetworkLoss = false
```

Add `onResume`/`onPause` handling (merge the cookie flush from Task 5 into this `onPause`):

```kotlin
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
```

(If Task 5's standalone `onPause` exists, replace it with this combined version.)

- [ ] **Step 3: Long-press settings dialog to change the server URL**

Add imports:

```kotlin
import android.widget.EditText
import androidx.appcompat.app.AlertDialog
```

Add a helper and wire a long-press listener (inside `onCreate`, after the WebView is created):

```kotlin
        webView.setOnLongClickListener {
            showServerDialog()
            true
        }
```

```kotlin
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
```

- [ ] **Step 4: Build to confirm it compiles**

Run: `cd android && ./gradlew assembleDebug`
Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 5: Manual verification (record in task report)**

1. Tap a wall cell to enlarge → press Back → overlay closes, wall remains.
2. With no overlay open, press Back once → "한 번 더 누르면 종료" toast; press Back again within 2s → app exits.
3. Toggle airplane mode on then off → the wall reloads and resumes within a few seconds.
4. Long-press the screen → dialog shows the current URL; change to another `https://` host and save → it loads; reopen the app → the new URL persists.

Expected: all four hold.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/java/net/hotelpos/opsview/MainActivity.kt
git commit -m "feat(android): back closes overlay/exits, network-regain reload, server-URL dialog"
```

---

### Task 7: CI — build + sign the APK and attach it to the GitHub Release

Add an `android` build job to the existing tag-driven release workflow and publish the APK as a release asset.

**Files:**
- Modify: `.github/workflows/release.yml` (add `build-android` job; extend the `release` job `needs` and `files`)

**Interfaces:**
- Consumes: repo secrets `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` (added by the maintainer). When `ANDROID_KEYSTORE_BASE64` is empty, the job builds an unsigned-debug APK so the workflow still succeeds (mirrors the existing "warn if signing key missing" pattern).
- Produces: a `*.apk` artifact named `opsview-android-…` downloaded by the `release` job (its existing `pattern: opsview-*` already matches) and attached via the `files:` list.

- [ ] **Step 1: Add the `build-android` job**

In `.github/workflows/release.yml`, add this job under `jobs:` (e.g., after `build-viewer`, before `release`):

```yaml
  build-android:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: "17"

      - name: Set up Android SDK
        uses: android-actions/setup-android@v3

      - name: Set version from tag
        shell: bash
        run: |
          TAG="${{ github.event.release.tag_name || github.ref_name }}"
          echo "APP_VERSION=${TAG#v}" >> "$GITHUB_ENV"

      - name: Build APK
        shell: bash
        working-directory: android
        env:
          KS_B64: ${{ secrets.ANDROID_KEYSTORE_BASE64 }}
          KS_PASS: ${{ secrets.ANDROID_KEYSTORE_PASSWORD }}
          KEY_ALIAS: ${{ secrets.ANDROID_KEY_ALIAS }}
          KEY_PASS: ${{ secrets.ANDROID_KEY_PASSWORD }}
        run: |
          if [ -n "$KS_B64" ]; then
            echo "$KS_B64" | base64 --decode > release.keystore
            ./gradlew :app:assembleRelease \
              -Pandroid.injected.signing.store.file="$PWD/release.keystore" \
              -Pandroid.injected.signing.store.password="$KS_PASS" \
              -Pandroid.injected.signing.key.alias="$KEY_ALIAS" \
              -Pandroid.injected.signing.key.password="$KEY_PASS"
            cp app/build/outputs/apk/release/app-release.apk "../opsview-android-${APP_VERSION}.apk"
          else
            echo "::warning::ANDROID_KEYSTORE_BASE64 not set; building unsigned debug APK."
            ./gradlew :app:assembleDebug
            cp app/build/outputs/apk/debug/app-debug.apk "../opsview-android-${APP_VERSION}-unsigned.apk"
          fi

      - uses: actions/upload-artifact@v4
        with:
          name: opsview-android
          path: opsview-android-*.apk
```

> Note: this requires the Gradle wrapper committed in Task 3 (`android/gradlew`, `android/gradle/wrapper/*`). The `setup-android` action provides the SDK and accepts licenses.

- [ ] **Step 2: Add `build-android` to the release job's `needs` and `files`**

In the `release:` job, change:

```yaml
  release:
    needs: [build-agent, build-viewer]
```

to:

```yaml
  release:
    needs: [build-agent, build-viewer, build-android]
```

And in the `softprops/action-gh-release@v2` step, add `artifacts/*.apk` to the `files:` list so it becomes:

```yaml
          files: |
            artifacts/*.dmg
            artifacts/*-setup.exe
            artifacts/*.tar.gz
            artifacts/*.zip
            artifacts/*.sig
            artifacts/*.apk
```

- [ ] **Step 3: Validate the workflow YAML**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml')); print('YAML OK')"`
Expected: `YAML OK`.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci(android): build, sign, and publish APK on v* release"
```

---

## Notes for the executor

- **Do not push tags.** Per the user's batching habit, commit all tasks but leave pushing/tagging to the user's explicit go-ahead.
- The Android tasks need the Android SDK locally; if it is unavailable in your environment, complete the code, run the JVM unit test task where possible (Task 4), and mark the build/manual steps as "verified in CI / pending device" in the task report rather than fabricating a pass.
- After all tasks: per subagent-driven-development, run the final whole-branch review, then `superpowers:finishing-a-development-branch`.
