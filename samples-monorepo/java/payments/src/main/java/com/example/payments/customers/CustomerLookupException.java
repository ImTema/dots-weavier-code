package com.example.payments.customers;

public final class CustomerLookupException extends RuntimeException {
    public CustomerLookupException(String message, Throwable cause) {
        super(message, cause);
    }
}
