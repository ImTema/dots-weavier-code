package com.example.shipping

import com.example.shipping.customers.OrdersCustomerDirectory
import com.example.shipping.http.shippingModule
import com.example.shipping.rates.ShippingRates
import io.ktor.client.HttpClient
import io.ktor.client.engine.cio.CIO
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.serialization.kotlinx.json.json
import io.ktor.server.engine.embeddedServer
import io.ktor.server.netty.Netty
import kotlinx.serialization.json.Json

/** Entry point: serves GET /rates over HTTP. */
fun main() {
    val port = System.getenv("PORT")?.toIntOrNull() ?: 8081
    val ordersUrl = System.getenv("ORDERS_URL") ?: "http://localhost:8080"

    val client = HttpClient(CIO) {
        install(ContentNegotiation) { json(Json { ignoreUnknownKeys = true }) }
        install(HttpTimeout) {
            connectTimeoutMillis = 2_000
            requestTimeoutMillis = 3_000
        }
        expectSuccess = false
    }
    val rates = ShippingRates(OrdersCustomerDirectory(client, ordersUrl))

    val server = embeddedServer(Netty, port = port) { shippingModule(rates) }
    Runtime.getRuntime().addShutdownHook(Thread {
        server.stop(gracePeriodMillis = 1_000, timeoutMillis = 5_000)
        client.close()
    })
    server.start(wait = true)
}
