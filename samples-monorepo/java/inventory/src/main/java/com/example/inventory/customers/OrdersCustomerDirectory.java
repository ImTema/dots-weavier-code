package com.example.inventory.customers;

import jakarta.inject.Singleton;
import jakarta.ws.rs.ProcessingException;
import jakarta.ws.rs.WebApplicationException;
import org.eclipse.microprofile.rest.client.inject.RestClient;

/** Resolves tiers through the orders HTTP API: GET /customers/{id}. */
@Singleton
public final class OrdersCustomerDirectory implements CustomerDirectory {
    private static final String STANDARD = "standard";

    private final OrdersApi orders;

    public OrdersCustomerDirectory(@RestClient OrdersApi orders) {
        this.orders = orders;
    }

    @Override
    public String tier(String customerId) {
        if (customerId == null || customerId.isBlank()) {
            return STANDARD;
        }
        try {
            OrdersApi.Customer c = orders.customer(customerId);
            return c.tier() == null || c.tier().isBlank() ? STANDARD : c.tier();
        } catch (WebApplicationException e) {
            if (e.getResponse().getStatus() == 404) {
                return STANDARD;
            }
            throw new CustomerLookupException("orders returned " + e.getResponse().getStatus(), e);
        } catch (ProcessingException e) {
            throw new CustomerLookupException("get customer " + customerId, e);
        }
    }
}
