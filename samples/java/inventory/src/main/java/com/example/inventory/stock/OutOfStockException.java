package com.example.inventory.stock;

public final class OutOfStockException extends RuntimeException {
    public OutOfStockException(String sku, int wanted, int available) {
        super("sku " + sku + ": wanted " + wanted + ", available " + available);
    }
}
