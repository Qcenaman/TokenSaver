plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val appVersionName = "0.2.3"
val appVersionCode = 13

android {
    namespace = "com.qcenaman.mobiletokensaver"
    compileSdk = 35
    defaultConfig {
        applicationId = "com.qcenaman.mobiletokensaver"
        minSdk = 30
        targetSdk = 35
        versionCode = appVersionCode
        versionName = appVersionName
        // 64-bit phones and tablets, and 32-bit ARM devices such as Android TVs
        ndk { abiFilters += listOf("arm64-v8a", "armeabi-v7a") }
    }
    // One committed keystore for every build, so each new APK installs over the previous one.
    signingConfigs {
        getByName("debug") {
            storeFile = rootProject.file("app/debug.keystore")
            storePassword = "android"; keyAlias = "androiddebugkey"; keyPassword = "android"
        }
    }
    buildTypes {
        debug { signingConfig = signingConfigs.getByName("debug") }
        release { isMinifyEnabled = false; signingConfig = signingConfigs.getByName("debug") }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures { compose = true; buildConfig = true }
    testOptions { unitTests.isReturnDefaultValues = true }
    // Native libs stay extracted (and executable) in nativeLibraryDir: the tunnel runs from there.
    packaging {
        jniLibs.useLegacyPackaging = true
        resources { excludes += setOf("META-INF/versions/9/OSGI-INF/MANIFEST.MF", "META-INF/DEPENDENCIES") }
    }
}

dependencies {
    implementation("com.github.MuntashirAkon:libadb-android:3.1.1")
    implementation("org.conscrypt:conscrypt-android:2.5.3")
    implementation("org.bouncycastle:bcpkix-jdk15to18:1.81")
    implementation("org.nanohttpd:nanohttpd:2.3.1")
    implementation("dev.rikka.shizuku:api:13.1.5")
    implementation("dev.rikka.shizuku:provider:13.1.5")
    implementation("androidx.security:security-crypto:1.1.0")
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    implementation(platform("androidx.compose:compose-bom:2024.12.01"))
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.ui:ui")
    testImplementation("junit:junit:4.13.2")
}

// The Tailscale Funnel tunnel (Go, tsnet) built as a static Linux binary per ABI and shipped as a native lib.
val tunnelDir = rootProject.file("tunnel")
val goExe = (findProperty("go.exe") as String?) ?: "go"
// ABI → GOARCH, GOARM
val tunnelAbis = mapOf("arm64-v8a" to ("arm64" to ""), "armeabi-v7a" to ("arm" to "7"))
val tunnelTasks = tunnelAbis.map { (abi, arch) ->
    val out = file("src/main/jniLibs/$abi/libtsfunnel.so")
    tasks.register<Exec>("buildTunnel-$abi") {
        onlyIf { findProperty("skipTunnel") == null }
        inputs.files(fileTree(tunnelDir) { include("*.go", "go.mod", "go.sum") }).withPropertyName("src")
        outputs.file(out)
        workingDir = tunnelDir
        environment("CGO_ENABLED", "0"); environment("GOOS", "linux"); environment("GOARCH", arch.first)
        if (arch.second.isNotEmpty()) environment("GOARM", arch.second)
        commandLine(goExe, "build", "-trimpath", "-ldflags=-s -w", "-o", out.absolutePath, ".")
        doFirst { out.parentFile.mkdirs() }
    }
}
val buildTunnel by tasks.registering { dependsOn(tunnelTasks) }
tasks.named("preBuild") { dependsOn(buildTunnel) }
