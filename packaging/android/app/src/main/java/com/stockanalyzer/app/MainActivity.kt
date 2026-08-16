package com.stockanalyzer.app

import android.annotation.SuppressLint
import android.os.Bundle
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity

/**
 * WebView 壳：加载 Go 后端提供的本地页面（http://127.0.0.1:8080）。
 * 退出时销毁后端进程；再次打开单实例复用（launchMode=singleTask）。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        GoServer.start(applicationContext)
        webView = WebView(this)
        setContentView(webView)
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            cacheMode = WebSettings.LOAD_DEFAULT
        }
        webView.webViewClient = object : WebViewClient() {
            // 保持应用内导航
            override fun shouldOverrideUrlLoading(view: WebView?, url: String?): Boolean {
                url ?: return false
                if (url.startsWith(GoServer.BASE_URL)) {
                    return false
                }
                // 外部链接用系统浏览器打开
                return true
            }

            // 后端未就绪/加载失败时给出提示，避免白屏
            override fun onReceivedError(
                view: WebView?,
                request: android.webkit.WebResourceRequest?,
                error: android.webkit.WebResourceError?
            ) {
                super.onReceivedError(view, request, error)
                if (request?.isForMainFrame == true) {
                    val html = "<html><body style='font-family:sans-serif;text-align:center;margin-top:40%'>" +
                        "<h3>本地服务启动中…</h3><p style='color:#666'>请稍候或重启应用再试</p></body></html>"
                    view?.loadDataWithBaseURL(null, html, "text/html", "utf-8", null)
                }
            }
        }
        webView.webChromeClient = WebChromeClient()
        webView.loadUrl(GoServer.BASE_URL)
    }

    override fun onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack()
        } else {
            super.onBackPressed()
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        // 应用退出时停止本地 Go 服务
        GoServer.stop()
    }
}
