package com.example.payments.charge;

public final class ChargeDeclinedException extends RuntimeException {
    public ChargeDeclinedException(String reason) {
        super(reason);
    }
}
