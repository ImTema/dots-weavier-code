package com.example.payments.customers;

import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.annotation.HttpExchange;

/** The orders HTTP API (example.com/samples/orders/api), as a Spring HTTP interface client. */
@HttpExchange("/customers")
public interface OrdersApi {
    @GetExchange("/{id}")
    Customer customer(@PathVariable String id);
}
