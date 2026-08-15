# Android APK 打包（Go 后端 + WebView 壳）

把 Go 后端（纯 Go、零 CGO）编译为 Android arm64 可执行文件，由 Android 应用
（Kotlin WebView 壳）在启动时拉起，监听 127.0.0.1:8080，WebView 加载本地页面。

## 目录结构

    packaging/android/
      build.sh                        一键构建（交叉编译 Go + Gradle 打包）
      settings.gradle.kts / build.gradle.kts / gradle.properties
      app/
        build.gradle.kts
        src/main/AndroidManifest.xml
        src/main/java/com/stockanalyzer/app/
          MainActivity.kt             WebView 壳（加载 http://127.0.0.1:8080）
          GoServer.kt                 拉起/停止本地 Go 服务（单实例复用）
        src/main/assets/bin/          （构建时生成）stockanalyzer-server

## 前置条件

- Android SDK（ANDROID_HOME）+ JDK 17+ + Gradle 8.x（或直接用 Android Studio 打开本目录）
- Go 1.26+（后端交叉编译）

## 构建步骤

    # 1) 交叉编译 Go 后端（零 CGO，arm64 可执行文件 → assets）
    cd backend && CGO_ENABLED=0 GOOS=android GOARCH=arm64       go build -o ../packaging/android/app/src/main/assets/bin/stockanalyzer-server ./cmd/server

    # 2) Gradle 打包（在 packaging/android 下）
    cd ../packaging/android && ./gradlew assembleDebug
    # 产物：app/build/outputs/apk/debug/app-debug.apk

    # 或一键：
    ./build.sh

## 运行时行为

- 首次启动：assets/bin/stockanalyzer-server 拷贝到应用私有目录并 chmod +x，
  以 STOCK_APP_HOME=<filesDir>/data 启动（数据/数据库全在应用私有目录，卸载即清）。
- WebView 加载 http://127.0.0.1:8080（index.html 入口，四页前端零改动）。
- 单实例：健康检查 /api/health 通过则复用已运行进程；退出应用销毁进程。
- 后端数据抓取仍访问外网数据源（东财/新浪/腾讯等），需联网权限（已声明 INTERNET）。

## 说明

- 数据库为应用私有目录下 etf.db（与桌面版同构，可自行迁移）。
- 支持任意架构：GOARCH=arm64（主流）、GOARCH=arm（32 位）、x86_64（模拟器）。
- 打包/调试签名默认使用 debug key；发布请配置 release 签名。
