package com.example.inventory.grpc;

import com.example.inventory.api.v1.InventoryService;
import com.example.inventory.api.v1.ReleaseRequest;
import com.example.inventory.api.v1.ReleaseResponse;
import com.example.inventory.api.v1.ReserveRequest;
import com.example.inventory.api.v1.ReserveResponse;
import com.example.inventory.customers.CustomerLookupException;
import com.example.inventory.reservation.LimitExceededException;
import com.example.inventory.reservation.ReservationService;
import com.example.inventory.stock.OutOfStockException;
import com.example.inventory.stock.UnknownSkuException;
import io.grpc.Status;
import io.quarkus.grpc.GrpcService;
import io.smallrye.common.annotation.Blocking;
import io.smallrye.mutiny.Uni;
import org.jboss.logging.Logger;

/** gRPC transport for inventory.v1.InventoryService. */
@GrpcService
public final class InventoryGrpcService implements InventoryService {
    private static final Logger log = Logger.getLogger(InventoryGrpcService.class);

    private final ReservationService reservations;

    public InventoryGrpcService(ReservationService reservations) {
        this.reservations = reservations;
    }

    /** Blocking: reserving makes an HTTP call to orders. */
    @Override
    @Blocking
    public Uni<ReserveResponse> reserve(ReserveRequest req) {
        var lines = req.getItemsList().stream()
                .map(it -> new ReservationService.Line(it.getSku(), it.getQty()))
                .toList();
        try {
            String id = reservations.reserve(req.getCustomerId(), lines);
            return Uni.createFrom().item(ReserveResponse.newBuilder().setReservationId(id).build());
        } catch (IllegalArgumentException e) {
            return fail(Status.INVALID_ARGUMENT, e);
        } catch (UnknownSkuException e) {
            return fail(Status.NOT_FOUND, e);
        } catch (OutOfStockException | LimitExceededException e) {
            return fail(Status.FAILED_PRECONDITION, e);
        } catch (CustomerLookupException e) {
            log.warn("customer lookup failed", e);
            return fail(Status.UNAVAILABLE, e);
        }
    }

    @Override
    public Uni<ReleaseResponse> release(ReleaseRequest req) {
        boolean released = reservations.release(req.getReservationId());
        return Uni.createFrom().item(ReleaseResponse.newBuilder().setReleased(released).build());
    }

    private static <T> Uni<T> fail(Status status, Exception e) {
        return Uni.createFrom().failure(status.withDescription(e.getMessage()).asRuntimeException());
    }
}
