import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

val bundledGodotProject = tasks.register<Sync>("bundleGodotProject") {
    from(layout.projectDirectory.dir("../../engine/godot-host"))
    into(layout.buildDirectory.dir("generated/godotAssets"))
    exclude(".godot/**", ".git/**", "*.import", "*.tmp", "README.md")
}

android {
    namespace = "dev.aisandbox.app"
    compileSdk = 36

    defaultConfig {
        applicationId = "dev.aisandbox.app"
        minSdk = 29
        targetSdk = 36
        versionCode = 1
        versionName = "0.0.1-prototype"

        ndk {
            abiFilters += listOf("arm64-v8a", "x86_64")
        }

        val props = Properties()
        val localFile = rootProject.file("local.properties")
        if (localFile.exists()) {
            localFile.inputStream().use { props.load(it) }
        }
        val cloudUrl = props.getProperty("SANDBOX_CLOUD_URL", "https://gamedev.sorapure.fun").trim()
        val cloudToken = props.getProperty("SANDBOX_CLOUD_TOKEN", "").trim()
            .replace("\\", "\\\\")
            .replace("\"", "\\\"")
        buildConfigField("String", "CLOUD_URL", "\"$cloudUrl\"")
        buildConfigField("String", "CLOUD_TOKEN", "\"$cloudToken\"")

        // Godot reads project.godot and trusted scripts directly from APK assets.
        aaptOptions {
            ignoreAssetsPattern = "!.svn:!.git:!.gitignore:!.ds_store:!*.scc:<dir>_*:!CVS:!thumbs.db:!picasa.ini:!*~"
        }
    }

    // AGP 9 does not accept TaskProvider as a SourceSet path. The directory is
    // fixed; preBuild ensures the trusted project has been copied before mergeAssets.
    sourceSets.getByName("main").assets.srcDir(layout.buildDirectory.dir("generated/godotAssets").get().asFile)

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

tasks.named("preBuild") {
    dependsOn(bundledGodotProject)
}

dependencies {
    implementation(project(":runtime-bridge"))
    implementation(project(":python-sandbox"))
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.activity:activity-compose:1.11.0")
    implementation(platform("androidx.compose:compose-bom:2025.07.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
}
