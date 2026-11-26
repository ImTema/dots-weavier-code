package com.example.payments.charge;

import com.example.payments.customers.Customer;
import com.example.payments.customers.CustomerDirectory;
import com.example.payments.limits.TierLimits;
import com.example.payments.risk.VelocityCheck;
import java.util.Optional;
import java.util.OptionalLong;
import org.springframework.stereotype.Service;

/** Charges orders: one charge per order, within the tier limit and the velocity check. */
@Service
public class ChargeService {
    private final CustomerDirectory customers;
    private final VelocityCheck velocity;
    private final ChargeLedger ledger;

    public ChargeService(CustomerDirectory customers, VelocityCheck velocity, ChargeLedger ledger) {
        this.customers = customers;
        this.velocity = velocity;
        this.ledger = ledger;
    }

    /**
     * @return the order's charge; a retried order returns its first charge
     * @throws IllegalArgumentException on a missing order or customer, or a non-positive amount
     * @throws ChargeDeclinedException over the tier limit or the velocity check
     */
    public Charge charge(String orderId, String customerId, long amountCents) {
        if (orderId == null || orderId.isBlank()) {
            throw new IllegalArgumentException("order_id is required");
        }
        if (customerId == null || customerId.isBlank()) {
            throw new IllegalArgumentException("customer_id is required");
        }
        if (amountCents <= 0) {
            throw new IllegalArgumentException("amount_cents must be positive");
        }
        return ledger.forOrder(orderId, () -> approve(orderId, customerId, amountCents));
    }

    public Optional<Charge> get(String id) {
        return ledger.byId(id);
    }

    private Charge approve(String orderId, String customerId, long amountCents) {
        Customer c = customers.find(customerId);
        OptionalLong limit = TierLimits.limitCents(c.tier());
        if (limit.isPresent() && amountCents > limit.getAsLong()) {
            throw new ChargeDeclinedException("over the " + c.tier() + " limit of " + limit.getAsLong() + " cents");
        }
        if (!velocity.admit(customerId)) {
            throw new ChargeDeclinedException("too many charges in the last minute");
        }
        return new Charge(ledger.nextId(), orderId, customerId, amountCents);
    }
}
