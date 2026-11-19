package com.example.inventory.stock;

import java.util.HashMap;
import java.util.Map;
import java.util.UUID;

/** Thread-safe in-memory stock with all-or-nothing reservations. */
public final class StockLedger {
    private final Map<String, Integer> onHand = new HashMap<>();
    private final Map<String, Map<String, Integer>> reservations = new HashMap<>();

    public StockLedger(Map<String, Integer> initial) {
        onHand.putAll(initial);
    }

    /**
     * Takes every requested quantity out of stock, or nothing.
     *
     * @return the reservation ID
     */
    public synchronized String reserve(Map<String, Integer> wanted) {
        for (var e : wanted.entrySet()) {
            Integer have = onHand.get(e.getKey());
            if (have == null) {
                throw new UnknownSkuException(e.getKey());
            } else if (have < e.getValue()) {
                throw new OutOfStockException(e.getKey(), e.getValue(), have);
            }
        }
        wanted.forEach((sku, qty) -> onHand.merge(sku, -qty, Integer::sum));
        String id = UUID.randomUUID().toString();
        reservations.put(id, Map.copyOf(wanted));
        return id;
    }

    /** Puts a reservation's stock back. Returns false if the ID is unknown or already released. */
    public synchronized boolean release(String reservationId) {
        Map<String, Integer> held = reservations.remove(reservationId);
        if (held == null) {
            return false;
        }
        held.forEach((sku, qty) -> onHand.merge(sku, qty, Integer::sum));
        return true;
    }

    public synchronized int available(String sku) {
        return onHand.getOrDefault(sku, 0);
    }
}
