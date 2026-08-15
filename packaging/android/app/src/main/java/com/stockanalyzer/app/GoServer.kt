package com.stockanalyzer.app

import android.content.Context
import android.util.Log
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/**
 * Go 后端本地服务管理：把 assets/bin/stockanalyzer-server 拷贝到应用私有目录，
 * 以 STOCK_APP_HOME=filesDir 启动（数据落应用私有目录），监听 127.0.0.1:8080。
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
        val pb = ProcessBuilder(
            bin.absolutePath,
            "--listen", "127.0.0.1:$PORT",
        )
        pb.environment()["STOCK_APP_HOME"] = File(context.filesDir, "data").absolutePath
        pb.environment()["STOCK_PORT"] = PORT.toString()
        pb.redirectErrorStream(true)
        process = pb.start()
        // 守护日志线程，避免 stdout 管道阻塞
        Thread {
            process?.inputStream?.bufferedReader()?.forEachLine { Log.d(TAG, it) }
        }.start()
        Log.i(TAG, "后端进程已启动 pid=${process?.pid()}")
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
