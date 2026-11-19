package com.example.shipping.rates

import com.example.shipping.customers.CustomerDirectory

data class RateQuote(val region: String, val cents: Long)

/** Prices shipping for a customer: looks them up in orders, then applies [RateCalculator]. */
class ShippingRates(private val customers: CustomerDirectory) {
    suspend fun quote(customerId: String, units: Int): RateQuote {
        require(units > 0) { "units must be positive" }
        // HTTP hop to orders: GET /customers/{id}.
        val customer = customers.find(customerId)
        return if (customer == null) {
            RateQuote(region = "", cents = RateCalculator.cents("", "standard", units))
        } else {
            RateQuote(customer.region, RateCalculator.cents(customer.region, customer.tier, units))
        }
    }
}
