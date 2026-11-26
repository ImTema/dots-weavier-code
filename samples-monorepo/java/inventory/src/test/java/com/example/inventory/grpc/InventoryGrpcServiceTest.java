package com.example.inventory.grpc;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.inventory.api.v1.InventoryService;
import com.example.inventory.api.v1.Item;
import com.example.inventory.api.v1.ReleaseRequest;
import com.example.inventory.api.v1.ReserveRequest;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.quarkus.grpc.GrpcClient;
import io.quarkus.test.junit.QuarkusTest;
import java.time.Duration;
import org.junit.jupiter.api.Test;

@QuarkusTest
class InventoryGrpcServiceTest {
    private static final Duration WAIT = Duration.ofSeconds(5);

    @GrpcClient
    InventoryService inventory;

    private static ReserveRequest request(String customer, String sku, int qty) {
        return ReserveRequest.newBuilder()
                .setCustomerId(customer)
                .addItems(Item.newBuilder().setSku(sku).setQty(qty))
                .build();
    }

    @Test
    void reserveThenRelease() {
        String id = inventory.reserve(request("c1", "a", 30)).await().atMost(WAIT).getReservationId();
        var release = ReleaseRequest.newBuilder().setReservationId(id).build();
        assertTrue(inventory.release(release).await().atMost(WAIT).getReleased());
        assertFalse(inventory.release(release).await().atMost(WAIT).getReleased());
    }

    @Test
    void errorsMapToStatusCodes() {
        assertCode(Status.Code.FAILED_PRECONDITION, request("c2", "a", 30)); // standard tier limit 20
        assertCode(Status.Code.FAILED_PRECONDITION, request("c1", "c", 1)); // out of stock
        assertCode(Status.Code.NOT_FOUND, request("c1", "zz", 1));
        assertCode(Status.Code.INVALID_ARGUMENT, request("c1", "a", 0));
    }

    private void assertCode(Status.Code want, ReserveRequest req) {
        var e = assertThrows(StatusRuntimeException.class, () -> inventory.reserve(req).await().atMost(WAIT));
        assertEquals(want, e.getStatus().getCode());
    }
}
