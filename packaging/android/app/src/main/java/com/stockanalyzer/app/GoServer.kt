package com.stockanalyzer.app

import android.content.Context
import android.content.res.AssetManager
import android.util.Log
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/**
 * Go 后端本地服务管理：把 assets/bin/stockanalyzer-server 拷贝到应用私有目录，
 * 以 STOCK_APP_HOME=filesDir 启动（数据落应用私有目录），监听 127.0.0.1:8080。
 * 前端静态资源（assets 根下的 index.html/js/css/vendor…）同步到 filesDir/static，
 * 并通过 STOCK_PROJECT_ROOT=filesDir 让 Go 端解析静态目录（config.projectRoot 优先读该 env）。
 * 单实例：进程已存活且健康检查通过则直接复用。
 */
object GoServer {
    private const val TAG = "GoServer"
    const val PORT = 8080
    const val BASE_URL = "http://127.0.0.1:$PORT"

    private var process: Process? = null

    fun start(context: Context) {
        if (isHealthy()) {
            Log.i(TAG, "后端已在运行，直接复用")
            return
        }
        try {
            startInternal(context)
        } catch (e: Exception) {
            // 任何启动异常只记日志，不让 Activity 崩溃（页面会显示加载失败提示）
            Log.e(TAG, "后端启动失败: ${e.message}", e)
        }
    }

    private fun startInternal(context: Context) {
        val bin = File(context.filesDir, "stockanalyzer-server")
        // 从 assets 拷贝二进制（assets 不可直接 exec，需落到私有目录）
        if (!bin.exists()) {
            context.assets.open("bin/stockanalyzer-server").use { input ->
                bin.outputStream().use { output -> input.copyTo(output) }
            }
            bin.setExecutable(true)
        }
        if (!bin.canExecute()) {
            bin.setExecutable(true)
        }
        // 同步前端静态资源（每次覆盖，体积小、保证随版本更新）
        syncStatic(context)
        val pb = ProcessBuilder(
            bin.absolutePath,
            "--listen", "127.0.0.1:$PORT",
        )
        pb.environment()["STOCK_APP_HOME"] = File(context.filesDir, "data").absolutePath
        pb.environment()["STOCK_PROJECT_ROOT"] = context.filesDir.absolutePath
        pb.environment()["STOCK_PORT"] = PORT.toString()
        pb.redirectErrorStream(true)
        process = pb.start()
        // 守护日志线程，避免 stdout 管道阻塞
        Thread {
            process?.inputStream?.bufferedReader()?.forEachLine { Log.d(TAG, it) }
        }.start()
        Log.i(TAG, "后端进程已启动")
    }

    /** 把 assets 根下除 bin/ 外的所有条目递归同步到 filesDir/static（Go 端 StaticDir）。 */
    private fun syncStatic(context: Context) {
        val dest = File(context.filesDir, "static")
        try {
            val entries = context.assets.list("") ?: return
            for (name in entries) {
                if (name == "bin") continue
                copyAssetRecursive(context.assets, name, File(dest, name))
            }
            Log.i(TAG, "静态资源已同步到 ${dest.absolutePath}")
        } catch (e: Exception) {
            Log.w(TAG, "静态资源同步失败: ${e.message}")
        }
    }

    private fun copyAssetRecursive(am: AssetManager, path: String, dest: File) {
        val children = am.list(path) ?: return
        if (children.isNotEmpty()) {
            for (c in children) {
                copyAssetRecursive(am, "$path/$c", File(dest, c))
            }
        } else {
            dest.parentFile?.mkdirs()
            am.open(path).use { input ->
                dest.outputStream().use { output -> input.copyTo(output) }
            }
        }
    }

    fun isHealthy(): Boolean = try {
        val conn = URL("$BASE_URL/api/health").openConnection() as HttpURLConnection
        conn.connectTimeout = 1500
        conn.readTimeout = 1500
        val ok = conn.responseCode == 200
        conn.disconnect()
        ok
    } catch (_: Exception) {
        false
    }

    fun stop() {
        process?.destroy()
        process = null
    }
}
