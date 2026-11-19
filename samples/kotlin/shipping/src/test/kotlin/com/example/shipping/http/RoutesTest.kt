package com.example.shipping.http

import com.example.shipping.customers.Customer
import com.example.shipping.customers.CustomerDirectory
import com.example.shipping.customers.CustomerLookupException
import com.example.shipping.rates.ShippingRates
import io.ktor.client.request.get
import io.ktor.client.statement.bodyAsText
import io.ktor.http.HttpStatusCode
import io.ktor.server.testing.testApplication
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class RoutesTest {
    private val directory = CustomerDirectory { id ->
        when (id) {
            "c1" -> Customer("c1", "gold", "EU")
            "c3" -> Customer("c3", "standard", "UK")
            "boom" -> throw CustomerLookupException("down")
            else -> null
        }
    }

    @Test
    fun rates() = testApplication {
        application { shippingModule(ShippingRates(directory)) }

        val gold = client.get("/rates?customer_id=c1&units=12")
        assertEquals(HttpStatusCode.OK, gold.status)
        assertTrue(""""cents":0""" in gold.bodyAsText())

        val standard = client.get("/rates?customer_id=c3&units=12")
        assertTrue(""""cents":1250""" in standard.bodyAsText(), standard.bodyAsText())

        assertEquals(HttpStatusCode.BadRequest, client.get("/rates?customer_id=c1").status)
        assertEquals(HttpStatusCode.BadRequest, client.get("/rates?customer_id=c1&units=0").status)
        assertEquals(HttpStatusCode.BadGateway, client.get("/rates?customer_id=boom&units=1").status)
    }
}
