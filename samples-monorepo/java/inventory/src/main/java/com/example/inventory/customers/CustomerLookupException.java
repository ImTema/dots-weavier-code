package com.example.inventory.customers;

public final class CustomerLookupException extends RuntimeException {
    public CustomerLookupException(String message, Throwable cause) {
        super(message, cause);
    }
}
