package com.example.payments.http;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.example.payments.customers.Customer;
import com.example.payments.customers.CustomerDirectory;
import com.example.payments.customers.CustomerLookupException;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Primary;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;

/** The HTTP transport over the real domain, with orders replaced by a stub directory. */
@SpringBootTest
@AutoConfigureMockMvc
class ChargeControllerTest {
    @TestConfiguration
    static class StubOrders {
        @Bean
        @Primary
        CustomerDirectory stubDirectory() {
            return id -> {
                if (id.equals("down")) {
                    throw new CustomerLookupException("orders down", null);
                }
                return Customer.anonymous(id);
            };
        }
    }

    @Autowired
    MockMvc mvc;

    private String body(String order, String customer, long cents) {
        return """
                {"order_id":"%s","customer_id":"%s","amount_cents":%d}""".formatted(order, customer, cents);
    }

    @Test
    void chargeThenGet() throws Exception {
        mvc.perform(post("/payments/charges").contentType(MediaType.APPLICATION_JSON).content(body("o-get", "c-get", 1500)))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.order_id").value("o-get"))
                .andExpect(jsonPath("$.amount_cents").value(1500))
                .andExpect(jsonPath("$.charge_id").isNotEmpty());
        String id = com.jayway.jsonpath.JsonPath.read(
                mvc.perform(post("/payments/charges").contentType(MediaType.APPLICATION_JSON).content(body("o-get", "c-get", 1500)))
                        .andReturn().getResponse().getContentAsString(), "$.charge_id");
        mvc.perform(get("/payments/charges/" + id)).andExpect(status().isOk()).andExpect(jsonPath("$.customer_id").value("c-get"));
        mvc.perform(get("/payments/charges/nope")).andExpect(status().isNotFound());
    }

    @Test
    void errorsMapToStatusCodes() throws Exception {
        mvc.perform(post("/payments/charges").contentType(MediaType.APPLICATION_JSON).content(body("o-bad", "c-bad", 0)))
                .andExpect(status().isBadRequest());
        mvc.perform(post("/payments/charges").contentType(MediaType.APPLICATION_JSON).content(body("o-big", "c-big", 50_001)))
                .andExpect(status().isPaymentRequired());
        mvc.perform(post("/payments/charges").contentType(MediaType.APPLICATION_JSON).content(body("o-down", "down", 100)))
                .andExpect(status().isBadGateway());
    }
}
