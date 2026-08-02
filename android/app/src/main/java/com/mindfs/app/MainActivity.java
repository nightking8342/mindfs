package com.mindfs.app;

import android.Manifest;
import android.app.DownloadManager;
import android.content.ActivityNotFoundException;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.content.res.Configuration;
import android.graphics.Color;
import android.net.Uri;
import android.net.http.SslError;
import android.os.Build;
import android.os.Bundle;
import android.util.Log;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.SslErrorHandler;
import android.webkit.WebSettings;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.widget.FrameLayout;
import androidx.core.app.ActivityCompat;
import androidx.core.content.ContextCompat;
import androidx.core.graphics.Insets;
import androidx.core.view.ViewCompat;
import androidx.core.view.WindowCompat;
import androidx.core.view.WindowInsetsCompat;
import androidx.core.view.WindowInsetsControllerCompat;
import com.getcapacitor.Bridge;
import com.getcapacitor.BridgeActivity;
import com.getcapacitor.BridgeWebViewClient;
import org.json.JSONArray;
import org.json.JSONObject;

public class MainActivity extends BridgeActivity {
    private static final String TAG = "MindFS";
    private static final int POST_NOTIFICATIONS_REQUEST_CODE = 19031;
    private static final String WEBVIEW_SETTINGS_PREFS = "mindfs_webview_settings";
    private static final String PREF_TEXT_ZOOM = "text_zoom";
    private static final int DEFAULT_TEXT_ZOOM = 100;
    private static final int MIN_TEXT_ZOOM = 50;
    private static final int MAX_TEXT_ZOOM = 200;
    private boolean notificationPermissionRequested = false;
    private int safeAreaTopPx = 0;
    private int safeAreaBottomPx = 0;
    private int imeBottomPx = 0;
    private View statusBarScrim = null;
    // 页面（web 层）最后推来的状态栏色。onResume / onConfigurationChanged 都会调
    // applySystemBarStyle()，若无条件按系统 uiMode 重刷，应用内选的主题会被打回去。
    private Integer pageStatusBarColor = null;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        registerPlugin(NativeDownloadPlugin.class);
        registerPlugin(NativeCacheControlPlugin.class);
        registerPlugin(LauncherNodeSyncPlugin.class);
        registerPlugin(ReplyPollerPlugin.class);
        super.onCreate(savedInstanceState);
        getBridge().setWebViewClient(new MindFSWebViewClient(getBridge()));
        WebView.setWebContentsDebuggingEnabled(true);
        CookieManager.getInstance().setAcceptCookie(true);
        getBridge().getWebView().getSettings().setMixedContentMode(
            WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE
        );
        applyStoredWebViewTextZoom();
        getBridge().getWebView().addJavascriptInterface(
            new LauncherNodeSyncBridge(),
            "MindFSLauncherNodeSync"
        );
        getBridge().getWebView().addJavascriptInterface(
            new NativeDownloadBridge(),
            "MindFSNativeDownload"
        );
        getBridge().getWebView().addJavascriptInterface(
            new ExternalBrowserBridge(),
            "MindFSExternalBrowser"
        );
        getBridge().getWebView().addJavascriptInterface(
            new ReplyPollerBridge(),
            "MindFSReplyPoller"
        );
        getBridge().getWebView().addJavascriptInterface(
            new AppInfoBridge(),
            "MindFSAppInfo"
        );
        getBridge().getWebView().addJavascriptInterface(
            new SystemBarsBridge(),
            "MindFSSystemBars"
        );
        getBridge().getWebView().addJavascriptInterface(
            new WebViewSettingsBridge(),
            "MindFSWebViewSettings"
        );
        clearPendingWebViewCacheIfNeeded();
        requestPostNotificationsIfNeeded();
        WindowCompat.setDecorFitsSystemWindows(getWindow(), false);
        applySystemBarStyle();
        ensureStatusBarScrim();
        installEdgeToEdgeInsetsOverride();
        fixWebViewMargin();
        dispatchReplySessionIntent(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        dispatchReplySessionIntent(intent);
    }

    @Override
    public void onResume() {
        super.onResume();
        requestPostNotificationsIfNeeded();
        pauseReplyPoller();
        clearCompletedReplyNotifications();
        applySystemBarStyle();
        ensureStatusBarScrim();
        installEdgeToEdgeInsetsOverride();
        fixWebViewMargin();
    }

    @Override
    public void onPause() {
        CookieManager.getInstance().flush();
        resumeReplyPollerFromCurrentPage();
        super.onPause();
    }

    @Override
    public void onConfigurationChanged(Configuration newConfig) {
        super.onConfigurationChanged(newConfig);
        applySystemBarStyle();
        notifyThemeChanged();
    }

    private void applySystemBarStyle() {
        getWindow().setStatusBarColor(Color.TRANSPARENT);
        getWindow().setNavigationBarColor(Color.TRANSPARENT);
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
            getWindow().setStatusBarContrastEnforced(false);
            getWindow().setNavigationBarContrastEnforced(false);
        }
        boolean darkMode =
            (getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK)
                == Configuration.UI_MODE_NIGHT_YES;
        WindowInsetsControllerCompat controller =
            WindowCompat.getInsetsController(getWindow(), getWindow().getDecorView());
        if (controller != null) {
            controller.setAppearanceLightStatusBars(!darkMode);
            controller.setAppearanceLightNavigationBars(false);
        }
        updateStatusBarScrimColor(
            pageStatusBarColor != null
                ? pageStatusBarColor
                : (darkMode ? Color.parseColor("#020617") : Color.WHITE)
        );
    }

    private void ensureStatusBarScrim() {
        if (statusBarScrim != null) {
            return;
        }
        View decorView = getWindow().getDecorView();
        if (!(decorView instanceof FrameLayout)) {
            return;
        }
        statusBarScrim = new View(this);
        statusBarScrim.setClickable(false);
        statusBarScrim.setFocusable(false);
        statusBarScrim.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        statusBarScrim.setBackgroundColor(Color.TRANSPARENT);
        FrameLayout.LayoutParams params = new FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT,
            0
        );
        params.gravity = android.view.Gravity.TOP;
        ((FrameLayout) decorView).addView(statusBarScrim, params);
    }

    private SharedPreferences webViewSettingsPrefs() {
        return getSharedPreferences(WEBVIEW_SETTINGS_PREFS, Context.MODE_PRIVATE);
    }

    private int clampTextZoom(int value) {
        if (value < MIN_TEXT_ZOOM) {
            return MIN_TEXT_ZOOM;
        }
        if (value > MAX_TEXT_ZOOM) {
            return MAX_TEXT_ZOOM;
        }
        return value;
    }

    private int getStoredWebViewTextZoom() {
        return clampTextZoom(webViewSettingsPrefs().getInt(PREF_TEXT_ZOOM, DEFAULT_TEXT_ZOOM));
    }

    private void applyStoredWebViewTextZoom() {
        applyWebViewTextZoom(getStoredWebViewTextZoom());
    }

    private void setStoredWebViewTextZoom(int value) {
        int zoom = clampTextZoom(value);
        webViewSettingsPrefs().edit().putInt(PREF_TEXT_ZOOM, zoom).apply();
        applyWebViewTextZoom(zoom);
    }

    private void applyWebViewTextZoom(int value) {
        int zoom = clampTextZoom(value);
        View webView = getBridge() == null ? null : getBridge().getWebView();
        if (webView instanceof WebView) {
            webView.post(() -> ((WebView) webView).getSettings().setTextZoom(zoom));
        }
    }

    private void updateStatusBarScrimHeight(int heightPx) {
        ensureStatusBarScrim();
        if (statusBarScrim == null) {
            return;
        }
        ViewGroup.LayoutParams params = statusBarScrim.getLayoutParams();
        if (params == null || params.height == heightPx) {
            return;
        }
        params.height = heightPx;
        statusBarScrim.setLayoutParams(params);
    }

    private void updateStatusBarScrimColor(int color) {
        ensureStatusBarScrim();
        if (statusBarScrim != null) {
            statusBarScrim.setBackgroundColor(color);
        }
        getWindow().setStatusBarColor(color);
        WindowInsetsControllerCompat controller =
            WindowCompat.getInsetsController(getWindow(), getWindow().getDecorView());
        if (controller != null) {
            controller.setAppearanceLightStatusBars(!isDarkColor(color));
        }
    }

    private boolean isDarkColor(int color) {
        double r = Color.red(color) / 255.0;
        double g = Color.green(color) / 255.0;
        double b = Color.blue(color) / 255.0;
        r = r <= 0.03928 ? r / 12.92 : Math.pow((r + 0.055) / 1.055, 2.4);
        g = g <= 0.03928 ? g / 12.92 : Math.pow((g + 0.055) / 1.055, 2.4);
        b = b <= 0.03928 ? b / 12.92 : Math.pow((b + 0.055) / 1.055, 2.4);
        return (0.2126 * r + 0.7152 * g + 0.0722 * b) < 0.5;
    }

    private void fixWebViewMargin() {
        View webView = getBridge().getWebView();
        if (webView == null) {
            return;
        }

        View parent = (View) webView.getParent();
        if (parent != null) {
            parent.setPadding(0, 0, 0, 0);
        }

        ViewGroup.LayoutParams rawParams = webView.getLayoutParams();
        if (rawParams instanceof ViewGroup.MarginLayoutParams) {
            ViewGroup.MarginLayoutParams params = (ViewGroup.MarginLayoutParams) rawParams;
            params.topMargin = 0;
            params.bottomMargin = 0;
            params.leftMargin = 0;
            params.rightMargin = 0;
            webView.setLayoutParams(params);
        }

        webView.post(() -> {
            ViewCompat.requestApplyInsets(webView);
            injectCurrentSafeAreaInsets();
        });
    }

    private void installEdgeToEdgeInsetsOverride() {
        View webView = getBridge().getWebView();
        if (webView == null) {
            return;
        }

        View parent = (View) webView.getParent();
        if (parent == null) {
            return;
        }

        ViewCompat.setOnApplyWindowInsetsListener(parent, (view, insets) -> {
            Insets systemInsets = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() | WindowInsetsCompat.Type.displayCutout()
            );
            safeAreaTopPx = 0;
            safeAreaBottomPx = systemInsets.bottom;
            // Android adjustResize already shrinks the WebView for the keyboard.
            // Do not also expose IME height to the page, or the web layout can reserve it twice.
            imeBottomPx = 0;
            updateStatusBarScrimHeight(systemInsets.top);
            view.setPadding(0, systemInsets.top, 0, 0);
            webView.setPadding(0, 0, 0, 0);
            injectCurrentSafeAreaInsets();
            return new WindowInsetsCompat.Builder(insets)
                .setInsets(WindowInsetsCompat.Type.systemBars() | WindowInsetsCompat.Type.displayCutout(), Insets.NONE)
                .build();
        });

        parent.post(() -> ViewCompat.requestApplyInsets(parent));
    }

    private void injectCurrentSafeAreaInsets() {
        injectSafeAreaInsets(safeAreaTopPx, safeAreaBottomPx, imeBottomPx);
    }

    private void injectCurrentSafeAreaInsetsAfterNavigation() {
        View webView = getBridge() == null ? null : getBridge().getWebView();
        if (!(webView instanceof WebView)) {
            return;
        }
        webView.post(() -> {
            injectCurrentSafeAreaInsets();
            syncStatusBarColorFromPage();
            webView.postDelayed(this::injectCurrentSafeAreaInsets, 120);
            webView.postDelayed(this::syncStatusBarColorFromPage, 120);
            webView.postDelayed(this::injectCurrentSafeAreaInsets, 360);
            webView.postDelayed(this::syncStatusBarColorFromPage, 360);
        });
    }

    private void syncStatusBarColorFromPage() {
        View webView = getBridge() == null ? null : getBridge().getWebView();
        if (!(webView instanceof WebView)) {
            return;
        }
        String script =
            "(function(){try{if(!window.MindFSSystemBars||!document||!document.elementFromPoint){return;}" +
            "function hexFromColor(v){v=(v||'').trim();if(!v||v==='transparent'){return '';}" +
            "if(/^#[0-9a-f]{6}$/i.test(v)){return v.toLowerCase();}" +
            "var m=v.match(/^rgba?\\(([^)]+)\\)$/i);if(!m){return '';}" +
            "var p=m[1].split(',').map(function(x){return parseFloat(x.trim());});" +
            "if(p.length<3||p[3]===0){return '';}" +
            "return '#'+p.slice(0,3).map(function(n){n=Math.max(0,Math.min(255,Math.round(n)||0));return n.toString(16).padStart(2,'0');}).join('');}" +
            "var x=Math.max(1,Math.min(window.innerWidth-1,window.innerWidth/2));var y=8;" +
            "var el=document.elementFromPoint(x,y);" +
            "while(el){var c=hexFromColor(getComputedStyle(el).backgroundColor);if(c){window.MindFSSystemBars.setStatusBarColor(c);return;}el=el.parentElement;}" +
            "var root=getComputedStyle(document.documentElement);var fallback=hexFromColor(root.getPropertyValue('--mindfs-topbar-bg'))||hexFromColor(root.getPropertyValue('--mindfs-system-bar-bg'));" +
            "if(fallback){window.MindFSSystemBars.setStatusBarColor(fallback);}" +
            "}catch(e){}})();";
        ((WebView) webView).evaluateJavascript(script, null);
    }

    private void injectSafeAreaInsets(int topPx, int bottomPx, int imeBottomPx) {
        View webView = getBridge() == null ? null : getBridge().getWebView();
        if (!(webView instanceof WebView)) {
            return;
        }

        String script = String.format(
            java.util.Locale.US,
            "(function(){if(!document||!document.documentElement||!document.documentElement.style){return;}var s=document.documentElement.style;s.setProperty('--mindfs-safe-area-top','%dpx');s.setProperty('--mindfs-safe-area-bottom','%dpx');s.setProperty('--mindfs-ime-bottom','%dpx');window.dispatchEvent(new CustomEvent('mindfs:safe-area-updated'));})();",
            Math.max(0, topPx),
            Math.max(0, bottomPx),
            Math.max(0, imeBottomPx)
        );
        ((WebView) webView).evaluateJavascript(script, null);
    }

    private void notifyThemeChanged() {
        View webView = getBridge().getWebView();
        if (webView == null) {
            return;
        }
        ((WebView) webView).evaluateJavascript(
            "(function(){if(!window){return;}window.dispatchEvent(new CustomEvent('mindfs:native-theme-changed'));})();",
            null
        );
    }

    private void dispatchReplySessionIntent(Intent intent) {
        if (intent == null) {
            return;
        }
        String rootId = intent.getStringExtra("rootId");
        String sessionKey = intent.getStringExtra("sessionKey");
        if (rootId == null || rootId.trim().isEmpty() || sessionKey == null || sessionKey.trim().isEmpty()) {
            return;
        }
        intent.removeExtra("rootId");
        intent.removeExtra("sessionKey");
        View webView = getBridge() == null ? null : getBridge().getWebView();
        if (!(webView instanceof WebView)) {
            return;
        }
        String script = String.format(
            java.util.Locale.US,
            "(function(){var detail={rootId:%s,sessionKey:%s};window.__mindfsPendingReplySession=detail;window.dispatchEvent(new CustomEvent('mindfs:open-reply-session',{detail:detail}));})();",
            JSONObject.quote(rootId.trim()),
            JSONObject.quote(sessionKey.trim())
        );
        webView.postDelayed(() -> ((WebView) webView).evaluateJavascript(script, null), 250);
    }

    private void clearPendingWebViewCacheIfNeeded() {
        if (!NativeCacheControlPlugin.shouldClearWebViewCacheOnNextLaunch(this)) {
            return;
        }
        try {
            View webView = getBridge().getWebView();
            if (webView instanceof WebView) {
                ((WebView) webView).clearCache(true);
            }
        } finally {
            NativeCacheControlPlugin.consumeClearWebViewCacheOnNextLaunch(this);
        }
    }

    private void clearCompletedReplyNotifications() {
        Intent intent = new Intent(this, ReplyPollerService.class);
        intent.setAction(ReplyPollerService.ACTION_CLEAR_COMPLETED);
        startService(intent);
    }

    private void pauseReplyPoller() {
        Intent intent = new Intent(this, ReplyPollerService.class);
        intent.setAction(ReplyPollerService.ACTION_PAUSE);
        startService(intent);
    }

    private void resumeReplyPollerFromCurrentPage() {
        String apiBaseUrl = currentHTTPOrigin();
        if (apiBaseUrl.isEmpty()) {
            Log.w(TAG, "skip reply poller resume: unable to derive API base URL");
            return;
        }
        Intent configure = new Intent(this, ReplyPollerService.class);
        configure.setAction(ReplyPollerService.ACTION_CONFIGURE);
        configure.putExtra(ReplyPollerService.EXTRA_API_BASE_URL, apiBaseUrl);
        startService(configure);

        Intent resume = new Intent(this, ReplyPollerService.class);
        resume.setAction(ReplyPollerService.ACTION_RESUME);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            ContextCompat.startForegroundService(this, resume);
        } else {
            startService(resume);
        }
    }

    private String currentHTTPOrigin() {
        View view = getBridge() == null ? null : getBridge().getWebView();
        if (!(view instanceof WebView)) {
            return "";
        }
        String rawURL = ((WebView) view).getUrl();
        if (rawURL == null || rawURL.trim().isEmpty()) {
            return "";
        }
        Uri uri = Uri.parse(rawURL.trim());
        String scheme = uri.getScheme();
        String authority = uri.getEncodedAuthority();
        if (
            authority == null ||
            authority.isEmpty() ||
            (!"http".equalsIgnoreCase(scheme) && !"https".equalsIgnoreCase(scheme))
        ) {
            return launcherNodeOrigin();
        }
        String host = uri.getHost();
        if ("localhost".equalsIgnoreCase(host) || "127.0.0.1".equals(host)) {
            return "";
        }
        return scheme.toLowerCase(java.util.Locale.US) + "://" + authority + relayPrefix(uri);
    }

    private String launcherNodeOrigin() {
        String raw = getSharedPreferences("mindfs_launcher_node_sync", Context.MODE_PRIVATE)
            .getString("launcher_nodes", "");
        if (raw == null || raw.trim().isEmpty()) {
            return "";
        }
        try {
            JSONArray nodes = new JSONArray(raw);
            String first = "";
            for (int i = 0; i < nodes.length(); i += 1) {
                JSONObject node = nodes.optJSONObject(i);
                if (node == null) {
                    continue;
                }
                String origin = apiBaseFromURL(node.optString("url", ""));
                if (origin.isEmpty()) {
                    continue;
                }
                if (first.isEmpty()) {
                    first = origin;
                }
                if (origin.startsWith("http://")) {
                    return origin;
                }
            }
            return first;
        } catch (Exception ex) {
            Log.w(TAG, "failed to parse launcher nodes for reply poller", ex);
            return "";
        }
    }

    private String apiBaseFromURL(String rawURL) {
        if (rawURL == null || rawURL.trim().isEmpty()) {
            return "";
        }
        Uri uri = Uri.parse(rawURL.trim());
        String scheme = uri.getScheme();
        String authority = uri.getEncodedAuthority();
        if (
            authority == null ||
            authority.isEmpty() ||
            (!"http".equalsIgnoreCase(scheme) && !"https".equalsIgnoreCase(scheme))
        ) {
            return "";
        }
        return scheme.toLowerCase(java.util.Locale.US) + "://" + authority + relayPrefix(uri);
    }

    private String relayPrefix(Uri uri) {
        String path = uri.getEncodedPath();
        if (path == null || path.isEmpty()) {
            return "";
        }
        String[] parts = path.split("/");
        if (parts.length >= 3 && "n".equals(parts[1]) && !parts[2].isEmpty()) {
            return "/n/" + parts[2];
        }
        return "";
    }

    static boolean isLocalNetworkHost(String host) {
        if (host == null) {
            return false;
        }
        String normalized = host.trim().toLowerCase(java.util.Locale.US);
        if (normalized.startsWith("[") && normalized.endsWith("]")) {
            normalized = normalized.substring(1, normalized.length() - 1);
        }
        if (
            "localhost".equals(normalized) ||
            "127.0.0.1".equals(normalized) ||
            "::1".equals(normalized)
        ) {
            return true;
        }
        if (
            normalized.startsWith("10.") ||
            normalized.startsWith("192.168.") ||
            normalized.startsWith("169.254.") ||
            normalized.startsWith("fc") ||
            normalized.startsWith("fd") ||
            normalized.startsWith("fe80:")
        ) {
            return true;
        }
        String[] parts = normalized.split("\\.");
        if (parts.length == 4) {
            try {
                int first = Integer.parseInt(parts[0]);
                int second = Integer.parseInt(parts[1]);
                return first == 172 && second >= 16 && second <= 31;
            } catch (NumberFormatException ignored) {
                return false;
            }
        }
        return normalized.endsWith(".local");
    }

    private class MindFSWebViewClient extends BridgeWebViewClient {
        MindFSWebViewClient(Bridge bridge) {
            super(bridge);
        }

        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
            Uri url = request == null ? null : request.getUrl();
            String scheme = url == null ? "" : url.getScheme();
            if ("http".equalsIgnoreCase(scheme) || "https".equalsIgnoreCase(scheme)) {
                return false;
            }
            return super.shouldOverrideUrlLoading(view, request);
        }

        @Override
        public void onPageFinished(WebView view, String url) {
            super.onPageFinished(view, url);
            injectCurrentSafeAreaInsetsAfterNavigation();
        }

        @Override
        public void onReceivedSslError(WebView view, SslErrorHandler handler, SslError error) {
            String host = "";
            if (error != null && error.getUrl() != null) {
                host = Uri.parse(error.getUrl()).getHost();
            }
            if (MainActivity.isLocalNetworkHost(host)) {
                handler.proceed();
                return;
            }
            super.onReceivedSslError(view, handler, error);
        }
    }

    private void requestPostNotificationsIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            return;
        }
        if (
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                == PackageManager.PERMISSION_GRANTED
        ) {
            return;
        }
        if (notificationPermissionRequested) {
            return;
        }
        notificationPermissionRequested = true;
        ActivityCompat.requestPermissions(
            this,
            new String[] { Manifest.permission.POST_NOTIFICATIONS },
            POST_NOTIFICATIONS_REQUEST_CODE
        );
    }

    private class LauncherNodeSyncBridge {
        @JavascriptInterface
        public void storeRelayNodes(String rawJSON) {
            LauncherNodeSyncPlugin.storeRelayNodesJSON(MainActivity.this, rawJSON);
        }
    }

    private class ExternalBrowserBridge {
        @JavascriptInterface
        public String open(String rawURL) {
            try {
                Uri uri = Uri.parse(rawURL == null ? "" : rawURL.trim());
                String scheme = uri.getScheme();
                if (!"http".equalsIgnoreCase(scheme) && !"https".equalsIgnoreCase(scheme)) {
                    return "Only http:// and https:// URLs can be opened externally";
                }
                Intent intent = new Intent(Intent.ACTION_VIEW, uri);
                intent.addCategory(Intent.CATEGORY_BROWSABLE);
                startActivity(intent);
                return "";
            } catch (ActivityNotFoundException ex) {
                Log.w(TAG, "No activity found to open external URL: " + rawURL, ex);
                return "No browser found to open this URL";
            } catch (Exception ex) {
                Log.w(TAG, "Failed to open external URL: " + rawURL, ex);
                return "Failed to open external URL: " + ex.getMessage();
            }
        }
    }

    private class ReplyPollerBridge {
        @JavascriptInterface
        public void configure(String rawJSON) {
            try {
                JSONObject payload = new JSONObject(rawJSON == null ? "{}" : rawJSON);
                String apiBaseUrl = safeURLBase(payload.optString("apiBaseUrl", ""));
                if (apiBaseUrl.isEmpty()) {
                    return;
                }
                Intent intent = new Intent(MainActivity.this, ReplyPollerService.class);
                intent.setAction(ReplyPollerService.ACTION_CONFIGURE);
                intent.putExtra(ReplyPollerService.EXTRA_API_BASE_URL, apiBaseUrl);
                if (payload.has("token")) {
                    intent.putExtra(ReplyPollerService.EXTRA_TOKEN, payload.optString("token", ""));
                }
                intent.putExtra(ReplyPollerService.EXTRA_E2EE_REQUIRED, payload.optBoolean("e2eeRequired", false));
                intent.putExtra(ReplyPollerService.EXTRA_E2EE_NODE_ID, payload.optString("e2eeNodeId", ""));
                intent.putExtra(ReplyPollerService.EXTRA_E2EE_CLIENT_ID, payload.optString("e2eeClientId", ""));
                intent.putExtra(ReplyPollerService.EXTRA_E2EE_TRANSPORT_KEY, payload.optString("e2eeTransportKey", ""));
                startService(intent);
            } catch (Exception ex) {
                Log.w(TAG, "failed to configure reply poller from JS bridge", ex);
            }
        }
    }

    private String safeURLBase(String rawURL) {
        String value = rawURL == null ? "" : rawURL.trim().replaceAll("/+$", "");
        if (value.isEmpty()) {
            return "";
        }
        try {
            Uri uri = Uri.parse(value);
            String scheme = uri.getScheme();
            String authority = uri.getEncodedAuthority();
            if (
                authority == null ||
                authority.isEmpty() ||
                (!"http".equalsIgnoreCase(scheme) && !"https".equalsIgnoreCase(scheme))
            ) {
                return "";
            }
            return value;
        } catch (Exception ignored) {
            return "";
        }
    }

    private class NativeDownloadBridge {
        @JavascriptInterface
        public String download(String url, String filename) {
            try {
                DownloadManager downloadManager =
                    (DownloadManager) getSystemService(Context.DOWNLOAD_SERVICE);
                if (downloadManager == null) {
                    return "DownloadManager is unavailable";
                }
                String safeFilename = NativeDownloadPlugin.sanitizeFilename(filename);
                if (safeFilename.isEmpty()) {
                    safeFilename = NativeDownloadPlugin.sanitizeFilename(
                        android.webkit.URLUtil.guessFileName(url, null, null)
                    );
                }
                if (safeFilename.isEmpty()) {
                    safeFilename = "download";
                }
                NativeDownloadPlugin.enqueueDownload(downloadManager, url, safeFilename);
                return "";
            } catch (Exception ex) {
                return "Failed to enqueue download: " + ex.getMessage();
            }
        }

        @JavascriptInterface
        public String saveBase64(String dataBase64, String filename, String mimeType) {
            try {
                String safeFilename = NativeDownloadPlugin.sanitizeFilename(filename);
                if (safeFilename.isEmpty()) {
                    safeFilename = "download";
                }
                String safeMimeType = mimeType == null || mimeType.trim().isEmpty()
                    ? "application/octet-stream"
                    : mimeType.trim();
                byte[] data = android.util.Base64.decode(dataBase64, android.util.Base64.DEFAULT);
                NativeDownloadPlugin.saveBytesToDownloads(MainActivity.this, data, safeFilename, safeMimeType);
                return "";
            } catch (Exception ex) {
                return "Failed to save download: " + ex.getMessage();
            }
        }
    }

    private class AppInfoBridge {
        @JavascriptInterface
        public String getInfo() {
            try {
                android.content.pm.PackageInfo info = getPackageManager().getPackageInfo(getPackageName(), 0);
                long versionCode;
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                    versionCode = info.getLongVersionCode();
                } else {
                    versionCode = info.versionCode;
                }
                String versionName = info.versionName == null ? "" : info.versionName;
                JSONObject payload = new JSONObject();
                payload.put("version", versionName);
                payload.put("build", String.valueOf(versionCode));
                return payload.toString();
            } catch (Exception ex) {
                return "{\"version\":\"\",\"build\":\"\"}";
            }
        }
    }

    private class SystemBarsBridge {
        @JavascriptInterface
        public void setStatusBarColor(String rawColor) {
            try {
                String value = rawColor == null ? "" : rawColor.trim();
                if (!value.matches("^#[0-9a-fA-F]{6}$")) {
                    return;
                }
                int color = Color.parseColor(value);
                runOnUiThread(() -> {
                    pageStatusBarColor = color;
                    updateStatusBarScrimColor(color);
                });
            } catch (Exception ex) {
                Log.w(TAG, "failed to set status bar color from page: " + rawColor, ex);
            }
        }
    }

    private class WebViewSettingsBridge {
        @JavascriptInterface
        public int getTextZoom() {
            return getStoredWebViewTextZoom();
        }

        @JavascriptInterface
        public void setTextZoom(int value) {
            setStoredWebViewTextZoom(value);
        }
    }
}
