# payments (Java, Spring Boot)

Charges orders. Serves HTTP on `:8082`; calls the Go `orders` service over
HTTP to learn each customer's tier. See `../../README.md` for how it fits with
the other services.

## Contract

`ChargeRequest` and `ChargeResponse` in `src/main/java/com/example/payments/http/ChargeController.java`
are the source of truth; Go `orders` mirrors them in `internal/payments`.
Field names on the wire are snake_case.

| Route | Does | Responses |
|-------|------|-----------|
| `POST /payments/charges` `{"order_id","customer_id","amount_cents"}` | Charges the order once. A retried order returns its first charge. | `201` charge, `400` bad request, `402` declined, `502` orders unreachable |
| `GET /payments/charges/{id}` | Returns a charge. | `200` charge, `404` unknown |

A charge is `{"charge_id","order_id","customer_id","amount_cents"}`; an error is `{"error"}`.

## Rules

- Most one charge may take, by tier: gold unlimited, silver 2,000.00,
  standard 500.00. Customers orders does not know are standard.
- A customer's fourth approved charge within one minute is declined.
- One charge per order: a retry with the same `order_id` returns the first
  charge and does not count again. A declined order may be retried.

## Layout

```
src/main/java/com/example/payments/
├── PaymentsApplication.java             entry point
├── http/ChargeController.java           HTTP transport, maps exceptions to status codes
├── charge/ChargeService.java            domain: validation, limits, velocity, one charge per order
├── charge/ChargeLedger.java             approved charges by ID and by order
├── limits/TierLimits.java               limit per tier
├── risk/VelocityCheck.java              charges per customer per minute
└── customers/
    ├── CustomerDirectory.java           port: customer lookup
    ├── OrdersApi.java                   Spring HTTP interface client for GET /customers/{id}
    ├── OrdersClientConfig.java          RestClient with timeouts behind OrdersApi
    └── OrdersCustomerDirectory.java     adapter: 404 means standard, other failures throw
```

## Run and test

With a JDK 25 `JAVA_HOME`:

```sh
./mvnw test
./mvnw package && java -jar target/payments-0.1.0.jar
```

Configuration through environment variables: `PORT` (default `8082`) and
`ORDERS_URL` (default `http://localhost:8080`). Timeouts to orders: 2s to
connect, 3s to read. `ChargeControllerTest` starts Spring and replaces the
orders call with a stub directory; `OrdersCustomerDirectoryTest` runs the
client against a local fake of orders.
