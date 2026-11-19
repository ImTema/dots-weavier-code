package com.example.inventory.grpc;

import com.example.inventory.customers.CustomerDirectory;
import io.quarkus.test.Mock;

/** Replaces the HTTP call to orders in @QuarkusTest. */
@Mock
public class StubCustomerDirectory implements CustomerDirectory {
    @Override
    public String tier(String customerId) {
        return "c1".equals(customerId) ? "gold" : "standard";
    }
}
