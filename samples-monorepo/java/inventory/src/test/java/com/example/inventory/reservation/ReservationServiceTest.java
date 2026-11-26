package com.example.inventory.reservation;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.example.inventory.stock.StockLedger;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class ReservationServiceTest {
    @ParameterizedTest
    @CsvSource({
        "gold,     60, true",
        "silver,   50, true",
        "silver,   51, false",
        "standard, 20, true",
        "standard, 21, false",
    })
    void tierLimitsApplyPerSku(String tier, int qty, boolean allowed) {
        var svc = new ReservationService(id -> tier, new StockLedger(Map.of("a", 100)));
        var lines = List.of(new ReservationService.Line("a", qty));
        if (allowed) {
            assertDoesNotThrow(() -> svc.reserve("c1", lines));
        } else {
            assertThrows(LimitExceededException.class, () -> svc.reserve("c1", lines));
        }
    }

    @ParameterizedTest
    @CsvSource({"'', 1", "a, 0"})
    void invalidLinesAreRejected(String sku, int qty) {
        var svc = new ReservationService(id -> "gold", new StockLedger(Map.of("a", 100)));
        assertThrows(IllegalArgumentException.class,
                () -> svc.reserve("c1", List.of(new ReservationService.Line(sku, qty))));
    }
}
