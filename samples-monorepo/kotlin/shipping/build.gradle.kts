plugins {
    kotlin("jvm") version "2.4.21"
    kotlin("plugin.serialization") version "2.4.21"
    application
}

group = "com.example"
version = "0.1.0"

kotlin {
    jvmToolchain(25)
}

repositories {
    mavenCentral()
}

val ktorVersion = "3.6.1"

dependencies {
    implementation("io.ktor:ktor-server-netty:$ktorVersion")
    implementation("io.ktor:ktor-server-content-negotiation:$ktorVersion")
    implementation("io.ktor:ktor-server-status-pages:$ktorVersion")
    implementation("io.ktor:ktor-serialization-kotlinx-json:$ktorVersion")
    implementation("io.ktor:ktor-client-cio:$ktorVersion")
    implementation("io.ktor:ktor-client-content-negotiation:$ktorVersion")
    implementation("ch.qos.logback:logback-classic:1.5.32")

    testImplementation(kotlin("test"))
    testImplementation("io.ktor:ktor-server-test-host:$ktorVersion")
}

application {
    mainClass = "com.example.shipping.ApplicationKt"
}

tasks.test {
    useJUnitPlatform()
}
