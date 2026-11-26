package com.example.payments.limits;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.OptionalLong;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class TierLimitsTest {
    @ParameterizedTest
    @CsvSource(nullValues = "null", value = {
            "gold,     -1",
            "silver,   200000",
            "standard, 50000",
            "unknown,  50000",
            "null,     50000",
    })
    void limitCents(String tier, long want) {
        assertEquals(want < 0 ? OptionalLong.empty() : OptionalLong.of(want), TierLimits.limitCents(tier));
    }
}
