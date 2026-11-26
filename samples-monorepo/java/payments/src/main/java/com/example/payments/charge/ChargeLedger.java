package com.example.payments.charge;

import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.function.Supplier;
import org.springframework.stereotype.Component;

/** Approved charges, by charge ID and by order ID. */
@Component
public class ChargeLedger {
    private final Map<String, Charge> byId = new HashMap<>();
    private final Map<String, Charge> byOrder = new HashMap<>();
    private int next;

    public synchronized Optional<Charge> byId(String id) {
        return Optional.ofNullable(byId.get(id));
    }

    /** @return the order's charge, recording the one approve makes if the order has none yet */
    public synchronized Charge forOrder(String orderId, Supplier<Charge> approve) {
        Charge c = byOrder.get(orderId);
        if (c != null) {
            return c;
        }
        c = approve.get();
        byId.put(c.id(), c);
        byOrder.put(orderId, c);
        return c;
    }

    synchronized String nextId() {
        return "ch-" + ++next;
    }
}
