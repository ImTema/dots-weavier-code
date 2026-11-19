# shipping (Kotlin, Ktor)

Prices delivery. Serves `GET /rates` over HTTP on `:8081`; calls the Go
`orders` service over HTTP to learn each customer's region and tier. Go
`pricing` calls it for every quote. See `../../README.md` for how it fits
with the other services.

## Contract

```
GET /rates?customer_id=c3&units=12
200 {"customer_id":"c3","region":"UK","cents":1250}
400 {"error":"..."}   units missing, not a number or not positive
502 {"error":"orders unavailable"}
```

`RateResponse` in `http/Routes.kt` is the contract; Go `pricing` mirrors it in
`internal/shipping`.

## Rules

- Base by region: EU 700, UK 900, US 500, anything else 1000 cents.
- Plus 50 cents for each unit beyond 5.
- Gold ships free, silver pays half. Unknown customers pay the full default.

## Layout

```
src/main/kotlin/com/example/shipping/
├── Application.kt               entry point: HTTP client, server, shutdown hook
├── http/Routes.kt               HTTP transport: GET /rates, error mapping
├── rates/RateCalculator.kt      domain: pure price rules
├── rates/ShippingRates.kt       domain: look up customer, then price
└── customers/CustomerDirectory.kt  port and Ktor client adapter for GET /customers/{id}
```

## Run and test

```sh
./gradlew test
./gradlew run
```

Configuration through environment variables: `PORT` (default `8081`) and
`ORDERS_URL` (default `http://localhost:8080`).
