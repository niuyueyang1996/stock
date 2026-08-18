plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.stockanalyzer.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.stockanalyzer.app"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.2.0"
        ndk { abiFilters += listOf("arm64-v8a") }
    }
    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    sourceSets {
        getByName("main") {
            // jniLibs：Go 后端二进制（arm64-v8a/libstockanalyzer.so，构建脚本/CI 交叉编译产出）。
            // 以 native 库方式打包才能 exec（Android 10+ 禁止 exec 应用私有目录，nativeLibraryDir 例外）
            assets.srcDirs("$rootDir/../../static")
        }
    }
    packaging {
        jniLibs { useLegacyPackaging = true }
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.webkit:webkit:1.11.0")
}
