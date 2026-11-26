package com.example.payments.customers;

/** A customer as orders serves it (example.com/samples/orders/api.Customer). */
public record Customer(String id, String tier, String region) {
    public static final String STANDARD = "standard";

    public static Customer anonymous(String id) {
        return new Customer(id, STANDARD, "");
    }
}
