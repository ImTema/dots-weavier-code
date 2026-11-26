package com.example.inventory.customers;

/** Resolves a customer's tier. */
public interface CustomerDirectory {
    /**
     * @return the tier, "standard" for anonymous or unknown customers
     * @throws CustomerLookupException when the directory cannot answer
     */
    String tier(String customerId);
}
