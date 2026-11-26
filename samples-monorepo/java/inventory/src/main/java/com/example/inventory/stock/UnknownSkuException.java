package com.example.inventory.stock;

public final class UnknownSkuException extends RuntimeException {
    public UnknownSkuException(String sku) {
        super("unknown sku " + sku);
    }
}
