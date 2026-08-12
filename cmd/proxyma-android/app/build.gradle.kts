import java.lang.reflect.Modifier
import java.net.URLClassLoader
import java.util.jar.JarFile

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val proxymaAarPath = providers.gradleProperty("proxymaAar")
    .orElse(providers.environmentVariable("PROXYMA_AAR"))
    .getOrElse("libs/proxyma.aar")
val proxymaAar = file(proxymaAarPath)

android {
    namespace = "com.proxyma.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.proxyma.android"
        minSdk = 21
        targetSdk = 35
        versionCode = 1
        versionName = "1.0"

        ndk {
            abiFilters.addAll(setOf("armeabi-v7a", "arm64-v8a", "x86", "x86_64"))
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.8.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.4")
    implementation("androidx.activity:activity-compose:1.9.1")

    // Compose
    val composeBom = platform("androidx.compose:compose-bom:2024.06.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")

    // Gson for parsing Go-side JSON
    implementation("com.google.code.gson:gson:2.11.0")

    // Device builds default to app/libs; CI/Make pass -PproxymaAar or PROXYMA_AAR.
    implementation(files(proxymaAar))

    testImplementation("junit:junit:4.13.2")
}

val verifyProxymaAar = tasks.register("verifyProxymaAar") {
    group = "verification"
    description = "Verify the selected Proxyma AAR exposes the required binding ABI"

    doLast {
        if (!proxymaAar.isFile) {
            throw GradleException(
                "Proxyma AAR not found at ${proxymaAar.absolutePath}. " +
                    "Generate it first or set -PproxymaAar=/path/proxyma.aar (or PROXYMA_AAR)."
            )
        }

        val classesJar = temporaryDir.resolve("classes.jar")
        try {
            JarFile(proxymaAar).use { aar ->
                val entry = aar.getJarEntry("classes.jar")
                    ?: throw GradleException("Proxyma AAR has no classes.jar: ${proxymaAar.absolutePath}")
                aar.getInputStream(entry).use { input ->
                    classesJar.outputStream().use { output -> input.copyTo(output) }
                }
            }

            URLClassLoader(arrayOf(classesJar.toURI().toURL()), null).use { loader ->
                val binding = Class.forName("proxyma_bind.Proxyma_bind", false, loader)

                fun requireStaticString(name: String, vararg parameters: Class<*>) {
                    val method = binding.getDeclaredMethod(name, *parameters)
                    if (!Modifier.isStatic(method.modifiers) || method.returnType != String::class.java) {
                        throw GradleException(
                            "Proxyma AAR method $name has an incompatible static signature or return type"
                        )
                    }
                }

                requireStaticString("runService", String::class.java, String::class.java)
                requireStaticString("resolveTaskResultPath", String::class.java)
                requireStaticString("cancelStream", String::class.java)
            }
        } catch (error: ReflectiveOperationException) {
            throw GradleException(
                "Proxyma AAR is stale or missing required binding methods: ${proxymaAar.absolutePath}",
                error
            )
        }
    }
}

tasks.configureEach {
    if (name == "preBuild" || name.startsWith("test") || name.startsWith("lint") || name.startsWith("assemble")) {
        dependsOn(verifyProxymaAar)
    }
}
