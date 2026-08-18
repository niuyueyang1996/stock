package com.stockanalyzer.app

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.OpenableColumns
import android.util.Log
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/**
 * WebView 壳：加载本地 Go 页面。Excel 导入不走 WebView file input（安卓经常点了没反应），
 * 由 JS 桥调系统选文件，再由壳进程 POST 到 127.0.0.1。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView

    private val pickExcel = registerForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        if (uri == null) {
            notifyJs(-1, "")
            return@registerForActivityResult
        }
        Thread { uploadExcel(uri) }.start()
    }

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
            allowContentAccess = true
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            cacheMode = WebSettings.LOAD_NO_CACHE
        }
        webView.addJavascriptInterface(AndroidBridge(), "AndroidBridge")
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView?, url: String?): Boolean {
                url ?: return false
                if (url.startsWith(GoServer.BASE_URL)) {
                    return false
                }
                return true
            }

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
        webView.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                webView: WebView?,
                filePathCallback: ValueCallback<Array<android.net.Uri>>?,
                fileChooserParams: FileChooserParams?
            ): Boolean {
                // 释放 WebView 回调，改走与按钮相同的原生选择器
                filePathCallback?.onReceiveValue(null)
                launchExcelPicker()
                return true
            }
        }
        webView.loadUrl(GoServer.BASE_URL)
    }

    inner class AndroidBridge {
        @JavascriptInterface
        fun pickExcel() {
            runOnUiThread { launchExcelPicker() }
        }

        /** 把 AI HTML 报告写成临时文件，优先唤起微信发送；无微信则系统分享。 */
        @JavascriptInterface
        fun shareHtml(html: String?) {
            if (html.isNullOrBlank()) {
                runOnUiThread { Toast.makeText(this@MainActivity, "没有可分享的报告", Toast.LENGTH_SHORT).show() }
                return
            }
            Thread {
                try {
                    val dir = File(cacheDir, "share").apply { mkdirs() }
                    val file = File(dir, "AI报告.html")
                    file.writeText(html, Charsets.UTF_8)
                    val uri = FileProvider.getUriForFile(
                        this@MainActivity,
                        "$packageName.fileprovider",
                        file,
                    )
                    runOnUiThread { launchShare(uri) }
                } catch (e: Exception) {
                    Log.e(TAG, "分享报告失败", e)
                    runOnUiThread {
                        Toast.makeText(this@MainActivity, "分享失败: ${e.message}", Toast.LENGTH_LONG).show()
                    }
                }
            }.start()
        }
    }

    private fun launchShare(uri: Uri) {
        val send = Intent(Intent.ACTION_SEND).apply {
            type = "*/*"
            putExtra(Intent.EXTRA_STREAM, uri)
            putExtra(Intent.EXTRA_SUBJECT, "AI 详细报告")
            clipData = ClipData.newRawUri("", uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        grantUriPermission("com.tencent.mm", uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        val wechat = Intent(send).setPackage("com.tencent.mm")
        try {
            startActivity(wechat)
        } catch (_: ActivityNotFoundException) {
            startActivity(Intent.createChooser(send, "分享报告"))
        }
    }

    private fun launchExcelPicker() {
        try {
            Toast.makeText(this, "请选择 Excel 文件", Toast.LENGTH_SHORT).show()
            pickExcel.launch("*/*")
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(this, "没有可用的文件选择器", Toast.LENGTH_LONG).show()
            notifyJs(0, "没有可用的文件选择器")
        }
    }

    private fun uploadExcel(uri: Uri) {
        try {
            val name = displayName(uri) ?: "holdings.xlsx"
            val bytes = contentResolver.openInputStream(uri)?.use { it.readBytes() }
                ?: throw IllegalStateException("无法读取文件")
            val boundary = "----StockImport${System.currentTimeMillis()}"
            val conn = (URL("${GoServer.BASE_URL}/api/holdings/import-excel").openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                doOutput = true
                connectTimeout = 15_000
                readTimeout = 60_000
                setRequestProperty("Content-Type", "multipart/form-data; boundary=$boundary")
            }
            conn.outputStream.use { out ->
                val safeName = name.replace("\"", "")
                out.write("--$boundary\r\n".toByteArray())
                out.write(
                    "Content-Disposition: form-data; name=\"file\"; filename=\"$safeName\"\r\n".toByteArray()
                )
                out.write("Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n\r\n".toByteArray())
                out.write(bytes)
                out.write("\r\n--$boundary--\r\n".toByteArray())
            }
            val code = conn.responseCode
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            val body = stream?.bufferedReader()?.readText().orEmpty()
            conn.disconnect()
            notifyJs(code, body)
        } catch (e: Exception) {
            Log.e(TAG, "Excel 上传失败", e)
            notifyJs(0, e.message ?: "上传失败")
        }
    }

    private fun displayName(uri: Uri): String? {
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) return c.getString(0)
        }
        return null
    }

    private fun notifyJs(status: Int, body: String) {
        val payload = JSONObject.quote(body)
        runOnUiThread {
            webView.evaluateJavascript(
                "window.onAndroidExcelImported&&window.onAndroidExcelImported($status,$payload)",
                null
            )
        }
    }

    override fun onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack()
        } else {
            super.onBackPressed()
        }
    }

    override fun onDestroy() {
        GoServer.stop()
    }

    companion object {
        private const val TAG = "MainActivity"
    }
}
