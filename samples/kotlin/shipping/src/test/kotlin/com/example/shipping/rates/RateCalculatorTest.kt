package com.example.shipping.rates

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

class RateCalculatorTest {
    @Test
    fun rates() {
        data class Case(val region: String, val tier: String, val units: Int, val want: Long)
        listOf(
            Case("EU", "standard", 5, 700),
            Case("EU", "standard", 12, 700 + 7 * 50),
            Case("US", "silver", 12, (500 + 7 * 50) / 2),
            Case("UK", "gold", 12, 0),
            Case("MARS", "standard", 1, 1_000),
        ).forEach { c ->
            assertEquals(c.want, RateCalculator.cents(c.region, c.tier, c.units), c.toString())
        }
    }

    @Test
    fun rejectsNonPositiveUnits() {
        assertFailsWith<IllegalArgumentException> { RateCalculator.cents("EU", "standard", 0) }
    }
}
