package com.example.payments.http;

import com.example.payments.charge.Charge;
import com.example.payments.charge.ChargeDeclinedException;
import com.example.payments.charge.ChargeService;
import com.example.payments.customers.CustomerLookupException;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

/** HTTP transport: POST /payments/charges and GET /payments/charges/{id}. */
@RestController
@RequestMapping("/payments")
public class ChargeController {
    /** JSON body of POST /payments/charges. orders (Go) mirrors it in internal/payments. */
    public record ChargeRequest(String orderId, String customerId, long amountCents) {}

    /** JSON body of a charge. orders (Go) mirrors it in internal/payments. */
    public record ChargeResponse(String chargeId, String orderId, String customerId, long amountCents) {
        static ChargeResponse of(Charge c) {
            return new ChargeResponse(c.id(), c.orderId(), c.customerId(), c.amountCents());
        }
    }

    public record ErrorResponse(String error) {}

    private final ChargeService charges;

    public ChargeController(ChargeService charges) {
        this.charges = charges;
    }

    @PostMapping("/charges")
    @ResponseStatus(HttpStatus.CREATED)
    public ChargeResponse charge(@RequestBody ChargeRequest req) {
        return ChargeResponse.of(charges.charge(req.orderId(), req.customerId(), req.amountCents()));
    }

    @GetMapping("/charges/{id}")
    public ResponseEntity<?> get(@PathVariable String id) {
        return charges.get(id)
                .<ResponseEntity<?>>map(c -> ResponseEntity.ok(ChargeResponse.of(c)))
                .orElseGet(() -> ResponseEntity.status(HttpStatus.NOT_FOUND).body(new ErrorResponse("no charge " + id)));
    }

    @ExceptionHandler(IllegalArgumentException.class)
    @ResponseStatus(HttpStatus.BAD_REQUEST)
    ErrorResponse badRequest(IllegalArgumentException e) {
        return new ErrorResponse(e.getMessage());
    }

    @ExceptionHandler(ChargeDeclinedException.class)
    @ResponseStatus(HttpStatus.PAYMENT_REQUIRED)
    ErrorResponse declined(ChargeDeclinedException e) {
        return new ErrorResponse("declined: " + e.getMessage());
    }

    @ExceptionHandler(CustomerLookupException.class)
    @ResponseStatus(HttpStatus.BAD_GATEWAY)
    ErrorResponse ordersUnavailable(CustomerLookupException e) {
        return new ErrorResponse("orders unavailable");
    }
}
