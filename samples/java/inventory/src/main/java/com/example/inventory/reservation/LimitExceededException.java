package com.example.inventory.reservation;

public final class LimitExceededException extends RuntimeException {
    public LimitExceededException(String sku, int wanted, int limit) {
        super("sku " + sku + ": " + wanted + " exceeds tier limit " + limit);
    }
}
