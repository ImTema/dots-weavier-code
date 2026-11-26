package com.example.payments.customers;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.client.RestClient;

/** OrdersCustomerDirectory against a local fake of the orders HTTP API. */
class OrdersCustomerDirectoryTest {
    HttpServer orders;
    OrdersCustomerDirectory dir;

    @BeforeEach
    void start() throws IOException {
        orders = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        orders.createContext("/customers/", ex -> {
            String id = ex.getRequestURI().getPath().substring("/customers/".length());
            int status;
            String body;
            switch (id) {
                case "c1" -> { status = 200; body = "{\"id\":\"c1\",\"tier\":\"gold\",\"region\":\"eu\",\"phone\":\"+1\"}"; }
                case "notier" -> { status = 200; body = "{\"id\":\"notier\"}"; }
                case "boom" -> { status = 500; body = "{}"; }
                default -> { status = 404; body = "{}"; }
            }
            byte[] b = body.getBytes(StandardCharsets.UTF_8);
            ex.getResponseHeaders().set("Content-Type", "application/json");
            ex.sendResponseHeaders(status, b.length);
            ex.getResponseBody().write(b);
            ex.close();
        });
        orders.start();
        String url = "http://127.0.0.1:" + orders.getAddress().getPort();
        dir = new OrdersCustomerDirectory(new OrdersClientConfig().ordersApi(RestClient.builder(), url, Duration.ofSeconds(1), Duration.ofSeconds(1)));
    }

    @AfterEach
    void stop() {
        orders.stop(0);
    }

    @Test
    void find() {
        assertEquals(new Customer("c1", "gold", "eu"), dir.find("c1"));
        assertEquals(Customer.anonymous("notier"), dir.find("notier"), "no tier is standard");
        assertEquals(Customer.anonymous("ghost"), dir.find("ghost"), "404 is standard");
        assertThrows(CustomerLookupException.class, () -> dir.find("boom"));
    }

    @Test
    void ordersDown() {
        orders.stop(0);
        assertThrows(CustomerLookupException.class, () -> dir.find("c1"));
    }
}
