package com.example.inventory.reservation;

import com.example.inventory.customers.CustomerDirectory;
import com.example.inventory.stock.StockLedger;
import jakarta.inject.Singleton;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Reserves stock for a customer within the limits of their tier. */
@Singleton
public final class ReservationService {
    private final CustomerDirectory customers;
    private final StockLedger ledger;

    public ReservationService(CustomerDirectory customers, StockLedger ledger) {
        this.customers = customers;
        this.ledger = ledger;
    }

    public record Line(String sku, int qty) {}

    /** @return the reservation ID */
    public String reserve(String customerId, List<Line> lines) {
        if (lines.isEmpty()) {
            throw new IllegalArgumentException("no items");
        }
        Map<String, Integer> wanted = new LinkedHashMap<>();
        for (Line l : lines) {
            if (l.sku() == null || l.sku().isBlank()) {
                throw new IllegalArgumentException("missing sku");
            } else if (l.qty() <= 0) {
                throw new IllegalArgumentException("sku " + l.sku() + ": qty must be positive");
            }
            wanted.merge(l.sku(), l.qty(), Integer::sum);
        }

        // HTTP hop to orders: GET /customers/{id}.
        int limit = TierLimits.perSku(customers.tier(customerId));
        wanted.forEach((sku, qty) -> {
            if (qty > limit) {
                throw new LimitExceededException(sku, qty, limit);
            }
        });
        return ledger.reserve(wanted);
    }

    public boolean release(String reservationId) {
        if (reservationId == null || reservationId.isBlank()) {
            return false;
        }
        return ledger.release(reservationId);
    }
}
