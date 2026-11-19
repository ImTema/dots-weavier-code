package com.example.shipping.customers

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.request.get
import io.ktor.http.HttpStatusCode
import io.ktor.http.encodeURLPathPart
import kotlinx.serialization.Serializable

/** A customer as served by the orders HTTP API (example.com/samples/orders/api). */
@Serializable
data class Customer(val id: String, val tier: String = "standard", val region: String = "")

class CustomerLookupException(message: String, cause: Throwable? = null) : RuntimeException(message, cause)

/** Finds customers. Returns null for anonymous or unknown customers. */
fun interface CustomerDirectory {
    suspend fun find(customerId: String): Customer?
}

/** [CustomerDirectory] backed by orders: GET {baseUrl}/customers/{id}. */
class OrdersCustomerDirectory(
    private val client: HttpClient,
    private val baseUrl: String,
) : CustomerDirectory {
    override suspend fun find(customerId: String): Customer? {
        if (customerId.isBlank()) return null
        val resp = try {
            client.get("${baseUrl.trimEnd('/')}/customers/${customerId.encodeURLPathPart()}")
        } catch (e: Exception) {
            throw CustomerLookupException("get customer $customerId", e)
        }
        return when (resp.status) {
            HttpStatusCode.OK -> resp.body<Customer>()
            HttpStatusCode.NotFound -> null
            else -> throw CustomerLookupException("orders returned ${resp.status}")
        }
    }
}
