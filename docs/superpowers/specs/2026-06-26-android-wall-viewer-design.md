# OpsView Android Wall Viewer — Design Spec

**Date:** 2026-06-26
**Status:** Approved (brainstorm), pending implementation plan

## Goal

A minimal Android app that lets a venue owner watch their CCTV **live wall**
(the relay-composited single-stream mosaic) on a phone, from anywhere, by
wrapping the relay's existing `/dashboard/wall` page in a WebView.

## Background / Key Insight

The relay **already** serves everything the phone needs:

- **`/dashboard/wall`** (`relay/dashboard_assets/wall.html` + `wall.js`, ~82
  lines total): one full-bleed `<video>` playing the GPU-composited wall
  mosaic, plus a transparent CSS-grid overlay of channel names; tapping a cell
  enlarges that channel's single live stream (`.wallbig` overlay), close via
  the ✕ button / tap-outside / Esc. Auto-reconnect with backoff. Re-fetches
  layout on `visibilitychange`. Already has `<meta name="viewport" ...>`.
- **Public reachability:** the relay is exposed via Cloudflare Tunnel
  (`ops.hotelpos.net` → `192.168.0.197:28186`, per `relay/docker-compose.yml`).
  No new networking needed.
- **Auth:** `/dashboard/wall` is guarded by `authedDashboard` (dashboard
  session cookie). Unauthed → `302` to `/dashboard` (login SPA). Login at
  `POST /dashboard/api/login` (admin password) issues a **persistent** cookie
  (`Expires = now + dashboardSessionTTL`, `Path=/dashboard`, `HttpOnly`,
  `SameSite=Strict`, `Secure` under TLS). Persistent ⇒ WebView keeps the user
  logged in across app restarts until TTL expiry.

Because the wall mosaic collapses N cameras into **one** H.264 stream, the
phone decodes a single `<video>` — no mobile decoder-cap problem. The wall
page's `enlarge()` is a CSS overlay (not the HTML5 Fullscreen API), so it
works inside the WebView with no `onShowCustomView` plumbing.

**Therefore the Android app is a thin native shell around this page.** The
only meaningful logic is window/lifecycle behavior a browser tab can't give:
keep-screen-on, immersive fullscreen, back-button semantics, and
network-change reconnect.

## Architecture

```
[Android APK — Kotlin]
  Activity (single)
   ├─ WebView (hardware-accelerated)
   │    loads  https://ops.hotelpos.net/dashboard/wall?agent=<id>
   │      ├─ unauthed → relay 302 → /dashboard?next=/dashboard/wall (login)
   │      └─ authed   → wall page (1 composited stream + overlay)
   ├─ FLAG_KEEP_SCREEN_ON   (monitoring app — screen must stay on)
   ├─ Immersive sticky fullscreen (hide status/nav bars)
   ├─ Back button: close .wallbig if open, else confirm-exit
   ├─ ConnectivityManager callback → reload on network regain
   └─ CookieManager (persistent) → stays logged in
            │
            ▼
   Relay (ops.hotelpos.net) — unchanged transport, two small additions below
```

## Components

### 1. Android shell — new `android/` Gradle module at repo root

**Identity / config**
- Application id: `net.hotelpos.opsview`
- `minSdk 26` (Android 8.0), `targetSdk 34`, Kotlin + AndroidX, single Activity.
- Start URL is a **configurable** string (default
  `https://ops.hotelpos.net/dashboard/wall`), stored in `SharedPreferences`,
  editable from a tiny settings sheet (long-press / overflow). Lets the owner
  point at a LAN IP or a different tunnel without a rebuild.

**Manifest**
- Permissions: `INTERNET`, `ACCESS_NETWORK_STATE`.
- `android:configChanges` includes `orientation|screenSize|keyboardHidden` so
  rotation does not destroy the WebView (no stream restart).
- `android:screenOrientation="fullSensor"` (wall is landscape content but allow
  portrait); `usesCleartextTraffic=false` (HTTPS only).

**WebView settings**
- `javaScriptEnabled = true`, `domStorageEnabled = true` (page uses
  `localStorage`), `mediaPlaybackRequiresUserGesture = false` (autoplay; the
  `<video>` is `muted autoplay playsinline`), hardware acceleration on.
- `CookieManager.setAcceptCookie(true)`; `flush()` in `onPause`. Persistent
  cookie ⇒ login survives restarts.
- `WebViewClient` keeps navigation in-app for the relay origin; opens any
  off-origin link in the system browser (defense-in-depth).

**Window behavior**
- `window.addFlags(FLAG_KEEP_SCREEN_ON)`.
- Immersive sticky fullscreen via `WindowInsetsControllerCompat`
  (hide system bars, swipe to reveal).

**Back button (`OnBackPressedCallback`)**
- If the enlarge overlay is open, close it by evaluating JS:
  `document.querySelector('#wallbig.show .wbig-close')?.click()`.
- Else show a "종료할까요?" confirm (double-back-to-exit), then `finish()`.
- Detect overlay-open by reading a small DOM signal: evaluate
  `document.getElementById('wallbig')?.classList.contains('show')`.

**Reconnect**
- Register a `ConnectivityManager.NetworkCallback`; on `onAvailable` after a
  loss, call `webView.reload()` (the wall page itself also self-reconnects the
  WS with backoff, so reload is the coarse safety net for long outages /
  expired cookie → it will land on login if needed).

### 2. Relay additions (small, benefit desktop too)

- **`?next=` login redirect.** `HandleWallPage` currently redirects unauthed
  users to `/dashboard` and the login SPA then lands on the dashboard index, not
  the wall. Add: unauthed wall request → `302 /dashboard?next=/dashboard/wall`;
  the login SPA, on successful login, navigates to a same-origin `next` param if
  present (whitelist to `/dashboard*` paths to avoid open-redirect). This gets
  the phone straight back to the wall after first login.
- **Mobile CSS polish** in `relay/dashboard_assets/` (`style.css` and/or the
  login SPA): bigger tap targets on `.wcell`, login form usable at phone width,
  `.wallbig` ✕ button finger-sized. No JS/behavior change to `wall.js`.

> Per-DVR walls (`walldvr<N>`) are **out of scope for MVP** — the dashboard wall
> page is whole-agent (`wall`), which for a single-agent venue shows all its
> channels. A DVR/agent picker can be added later if a multi-DVR owner needs it.

### 3. Distribution

- Build a signed **APK**, attached to the GitHub Release (sideload — no Play
  Store). Add an `android` job to the existing tag-driven `release.yml`
  (Gradle `assembleRelease`, sign with a keystore from CI secrets, upload as a
  release asset). Versioned off the same `v*` tag as the rest of OpsView.

## Auth flow (end to end)

1. App opens `…/dashboard/wall`.
2. No/expired cookie → relay `302 /dashboard?next=/dashboard/wall` → login SPA.
3. Owner enters admin password → `POST /dashboard/api/login` → persistent cookie
   set → SPA navigates to `next` → wall page.
4. Subsequent launches: cookie valid → wall loads directly until TTL expiry.

## Out of Scope (YAGNI — explicitly deferred)

- Push notifications for motion/events (motion detection exists relay-side; a
  later phase can add FCM + a relay webhook).
- Recording/timeline playback (live wall only).
- Remote screen-share control side of OpsView.
- Per-DVR / multi-agent picker, drag-reorder, adaptive-quality UI (the desktop
  viewer owns those; `wall.js` already self-manages reconnect).

## Testing

- **Android shell (instrumented / Robolectric where possible):** back-button
  closes overlay vs. exits; keep-screen-on flag set; cookie persists across
  Activity recreation; off-origin link escapes to browser; start-URL override
  round-trips through SharedPreferences.
- **Relay (Go unit tests):** `?next=` redirect issued for unauthed wall request;
  `next` sanitized to same-origin `/dashboard*` (open-redirect rejected);
  authed wall request still serves `wall.html`.
- **Manual on device:** login → wall renders single stream; tap cell enlarges /
  back closes; airplane-mode toggle triggers reconnect; rotate keeps stream;
  app relaunch stays logged in.

## Open Considerations (non-blocking)

- `dashboardSessionTTL` length sets how often the owner re-logs in. If it's
  short, a later enhancement could issue a longer-lived cookie for the mobile
  user-agent. Accept current TTL for MVP.
- APK signing key management in CI (new keystore secret).

## Related Memory

`live-wall-mosaic` (the wall mosaic this app surfaces), `deployment-topology`
(relay on Unraid + Cloudflare Tunnel), `viewer-fix-push-policy` (this is new
Android + relay work, not the Wails viewer — but treat releases as
commit-until-explicit per the user's batching habit).
