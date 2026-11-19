package com.example.inventory.customers;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import org.eclipse.microprofile.rest.client.inject.RegisterRestClient;

/** The orders HTTP API (example.com/samples/orders/api). */
@Path("/customers")
@RegisterRestClient(configKey = "orders")
public interface OrdersApi {
    @JsonIgnoreProperties(ignoreUnknown = true)
    record Customer(String id, String tier, String region) {}

    @GET
    @Path("/{id}")
    Customer customer(@PathParam("id") String id);
}
