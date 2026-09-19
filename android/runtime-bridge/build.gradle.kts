plugins {
    id("com.android.library")
}

android {
    namespace = "dev.aisandbox.runtime"
    compileSdk = 36
    defaultConfig { minSdk = 29 }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    api("org.godotengine:godot:4.6.1.stable")
}
