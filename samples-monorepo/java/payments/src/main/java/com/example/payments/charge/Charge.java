package com.example.payments.charge;

/** An approved charge. */
public record Charge(String id, String orderId, String customerId, long amountCents) {}
