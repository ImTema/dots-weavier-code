package com.example.inventory.stock;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.Map;
import org.junit.jupiter.api.Test;

class StockLedgerTest {
    @Test
    void reserveAndReleaseRestoresStock() {
        var ledger = new StockLedger(Map.of("a", 10));
        String id = ledger.reserve(Map.of("a", 4));
        assertEquals(6, ledger.available("a"));
        assertTrue(ledger.release(id));
        assertEquals(10, ledger.available("a"));
        assertFalse(ledger.release(id), "second release is a no-op");
    }

    @Test
    void reserveIsAllOrNothing() {
        var ledger = new StockLedger(Map.of("a", 10, "b", 1));
        assertThrows(OutOfStockException.class, () -> ledger.reserve(Map.of("a", 5, "b", 2)));
        assertEquals(10, ledger.available("a"));
        assertEquals(1, ledger.available("b"));
    }

    @Test
    void unknownSkuIsRejected() {
        var ledger = new StockLedger(Map.of());
        assertThrows(UnknownSkuException.class, () -> ledger.reserve(Map.of("zz", 1)));
    }
}
