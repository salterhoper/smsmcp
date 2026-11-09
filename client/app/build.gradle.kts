import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
}

/*
 * Release signing. The same key must sign every release, or Android refuses
 * the update and users have to uninstall (losing the pairing) to upgrade.
 *
 * CI:    SMSMCP_KEYSTORE_FILE, SMSMCP_KEYSTORE_PASSWORD, SMSMCP_KEY_ALIAS, SMSMCP_KEY_PASSWORD
 * Local: client/keystore.properties (git-ignored) with storeFile, storePassword, keyAlias, keyPassword
 * See docs/RELEASING.md.
 */
val keystoreProps = Properties().apply {
    val f = rootProject.file("keystore.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}
fun signing(env: String, prop: String): String? =
    System.getenv(env)?.takeIf { it.isNotBlank() } ?: keystoreProps.getProperty(prop)

val releaseStoreFile = signing("SMSMCP_KEYSTORE_FILE", "storeFile")

// Release versions come from the git tag in CI: -PversionName=1.2.3 -PversionCode=10203
val appVersionName = (findProperty("versionName") as String?) ?: "0.1.0"
val appVersionCode = (findProperty("versionCode") as String?)?.toInt() ?: 1

android {
    namespace = "com.salterhoper.smsmcp.gateway"
    compileSdk = 37

    defaultConfig {
        applicationId = "com.salterhoper.smsmcp.gateway"
        minSdk = 26
        targetSdk = 36
        versionCode = appVersionCode
        versionName = appVersionName
    }

    signingConfigs {
        if (releaseStoreFile != null) {
            create("release") {
                storeFile = file(releaseStoreFile)
                storePassword = signing("SMSMCP_KEYSTORE_PASSWORD", "storePassword")
                keyAlias = signing("SMSMCP_KEY_ALIAS", "keyAlias")
                keyPassword = signing("SMSMCP_KEY_PASSWORD", "keyPassword")
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
        }
        release {
            // Unsigned when no key is configured (fine for local checks; CI refuses to publish it).
            signingConfig = signingConfigs.findByName("release")
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        buildConfig = true
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.ktx)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.okhttp)
    implementation(libs.zxing.embedded)

    testImplementation(libs.junit)
}
