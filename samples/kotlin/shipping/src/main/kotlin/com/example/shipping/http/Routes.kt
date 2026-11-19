package com.example.shipping.http

import com.example.shipping.customers.CustomerLookupException
import com.example.shipping.rates.ShippingRates
import io.ktor.http.HttpStatusCode
import io.ktor.serialization.kotlinx.json.json
import io.ktor.server.application.Application
import io.ktor.server.application.install
import io.ktor.server.application.log
import io.ktor.server.plugins.BadRequestException
import io.ktor.server.plugins.contentnegotiation.ContentNegotiation
import io.ktor.server.plugins.statuspages.StatusPages
import io.ktor.server.response.respond
import io.ktor.server.routing.get
import io.ktor.server.routing.routing
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** JSON body of GET /rates. pricing (Go) decodes it in internal/shipping. */
@Serializable
data class RateResponse(
    @SerialName("customer_id") val customerId: String,
    val region: String,
    val cents: Long,
)

@Serializable
data class ErrorResponse(val error: String)

/** HTTP transport: GET /rates?customer_id=ID&units=N. */
fun Application.shippingModule(rates: ShippingRates) {
    install(ContentNegotiation) { json() }
    install(StatusPages) {
        exception<BadRequestException> { call, e ->
            call.respond(HttpStatusCode.BadRequest, ErrorResponse(e.message ?: "bad request"))
        }
        exception<IllegalArgumentException> { call, e ->
            call.respond(HttpStatusCode.BadRequest, ErrorResponse(e.message ?: "bad request"))
        }
        exception<CustomerLookupException> { call, e ->
            call.application.log.warn("customer lookup failed", e)
            call.respond(HttpStatusCode.BadGateway, ErrorResponse("orders unavailable"))
        }
    }
    routing {
        get("/rates") {
            val customerId = call.request.queryParameters["customer_id"].orEmpty()
            val units = call.request.queryParameters["units"]?.toIntOrNull()
                ?: throw BadRequestException("units must be an integer")
            val quote = rates.quote(customerId, units)
            call.respond(RateResponse(customerId, quote.region, quote.cents))
        }
    }
}
