package com.example.payments.charge;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.example.payments.customers.Customer;
import com.example.payments.customers.CustomerDirectory;
import com.example.payments.risk.VelocityCheck;
import java.time.Clock;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class ChargeServiceTest {
    static final CustomerDirectory CUSTOMERS = id -> Map.of(
            "gold", new Customer("gold", "gold", "eu"),
            "silver", new Customer("silver", "silver", "us")).getOrDefault(id, Customer.anonymous(id));

    static ChargeService service() {
        return new ChargeService(CUSTOMERS, new VelocityCheck(Clock.systemUTC()), new ChargeLedger());
    }

    @ParameterizedTest
    @CsvSource({
            // customer, amount, outcome
            "nobody,   50000,   ok",
            "nobody,   50001,   declined",
            "silver,   200000,  ok",
            "silver,   200001,  declined",
            "gold,     9999999, ok",
            "nobody,   0,       bad",
            "nobody,   -5,      bad",
    })
    void charge(String customer, long amount, String outcome) {
        var s = service();
        switch (outcome) {
            case "ok" -> assertEquals(amount, s.charge("o1", customer, amount).amountCents());
            case "declined" -> assertThrows(ChargeDeclinedException.class, () -> s.charge("o1", customer, amount));
            default -> assertThrows(IllegalArgumentException.class, () -> s.charge("o1", customer, amount));
        }
    }

    @Test
    void missingIdsAreBadRequests() {
        var s = service();
        assertThrows(IllegalArgumentException.class, () -> s.charge("", "c1", 100));
        assertThrows(IllegalArgumentException.class, () -> s.charge("o1", " ", 100));
    }

    @Test
    void retriedOrderReturnsItsFirstCharge() {
        var s = service();
        Charge first = s.charge("o1", "c1", 100);
        assertEquals(first, s.charge("o1", "c1", 999), "same order, first charge kept");
        assertEquals(first, s.get(first.id()).orElseThrow());
        // A retry is not a new charge, so it does not count towards the velocity check.
        s.charge("o2", "c1", 100);
        s.charge("o3", "c1", 100);
        assertThrows(ChargeDeclinedException.class, () -> s.charge("o4", "c1", 100));
    }

    @Test
    void declinedOrderMayBeRetried() {
        var s = service();
        assertThrows(ChargeDeclinedException.class, () -> s.charge("o1", "c1", 60_000));
        assertEquals(40_000, s.charge("o1", "c1", 40_000).amountCents());
    }
}
