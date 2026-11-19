package com.example.shipping.rates

/** Shipping price rules. Pure: no I/O. */
object RateCalculator {
    private val baseByRegion = mapOf("EU" to 700L, "UK" to 900L, "US" to 500L)
    private const val DEFAULT_BASE = 1_000L
    private const val FREE_UNITS = 5
    private const val PER_EXTRA_UNIT = 50L

    /** Cents to ship [units] to [region] for a customer of [tier]. */
    fun cents(region: String, tier: String, units: Int): Long {
        require(units > 0) { "units must be positive" }
        val base = baseByRegion[region] ?: DEFAULT_BASE
        val extra = if (units > FREE_UNITS) (units - FREE_UNITS) * PER_EXTRA_UNIT else 0L
        return when (tier) {
            "gold" -> 0L
            "silver" -> (base + extra) / 2
            else -> base + extra
        }
    }
}
