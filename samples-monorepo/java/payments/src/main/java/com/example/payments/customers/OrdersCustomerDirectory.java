package com.example.payments.customers;

import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Component;
import org.springframework.web.client.ResourceAccessException;
import org.springframework.web.client.RestClientResponseException;

/** Looks customers up through the orders HTTP API: GET /customers/{id}. */
@Component
public class OrdersCustomerDirectory implements CustomerDirectory {
    private final OrdersApi orders;

    public OrdersCustomerDirectory(OrdersApi orders) {
        this.orders = orders;
    }

    @Override
    public Customer find(String customerId) {
        try {
            Customer c = orders.customer(customerId);
            if (c == null || c.tier() == null || c.tier().isBlank()) {
                return Customer.anonymous(customerId);
            }
            return c;
        } catch (RestClientResponseException e) {
            if (e.getStatusCode().value() == HttpStatus.NOT_FOUND.value()) {
                return Customer.anonymous(customerId);
            }
            throw new CustomerLookupException("orders returned " + e.getStatusCode().value(), e);
        } catch (ResourceAccessException e) {
            throw new CustomerLookupException("get customer " + customerId, e);
        }
    }
}
