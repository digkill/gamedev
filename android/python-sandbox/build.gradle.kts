plugins {
    id("com.android.library")
}

android {
    namespace = "dev.aisandbox.python"
    compileSdk = 36
    defaultConfig { minSdk = 29 }
    buildFeatures { aidl = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
