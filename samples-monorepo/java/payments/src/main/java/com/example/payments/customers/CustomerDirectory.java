package com.example.payments.customers;

/** Looks customers up. */
public interface CustomerDirectory {
    /**
     * @return the customer, standard tier when orders does not know them
     * @throws CustomerLookupException when the directory cannot answer
     */
    Customer find(String customerId);
}
