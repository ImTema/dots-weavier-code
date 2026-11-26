# Scaffolding a sample project

`samples-monorepo/` holds small services in several languages that call each other. They
are a **known target** for dotweavier: every hop is documented, so a trace can
be checked against what the code really does. A new sample joins that
domain as one more service in the language the user picks.

`samples-monorepo/README.md` is the single source of truth for the current services,
ports and hops. Read it first; this file covers only what it does not.

## Steps

### 1. Fix the language and the hops

Take the language (and framework, if the user names one) from the user's
request; if the request names none, ask. Then pick a domain role that fits the
order flow in `samples-monorepo/README.md` and list the hops the new service adds.

Done when you have written down, for every hop: protocol (gRPC, HTTP or Pub/Sub),
caller symbol, callee symbol, and which project owns the contract. The list
has at least one **inbound** hop (an existing service calls the new one) and
at least one **outbound** hop (the new one calls an existing service). Show
the list to the user before writing code.

### 2. Define the contracts

The callee owns the contract:

- **gRPC**: the proto lives in the callee's project (`api/<name>/v1/` in Go,
  `src/main/proto/<name>/v1/` on the JVM). Consumers generate their client from
  that file. Set `go_package` to the consuming Go module's path when a Go
  project calls it, as `inventory.proto` does.
- **HTTP**: the callee defines the request and response types. Go consumers
  import an `api` package when the callee is Go; otherwise they mirror the
  type in their client package with a comment naming the source. Field names
  on the wire are snake_case.
- **Pub/Sub**: the publisher owns the event proto and the topic name, as a
  constant next to the generated code (`orders/api/events/v1/topic.go`). The
  subscriber binds its subscription to that constant, so both ends name the
  topic in code. The publisher creates the topic; the subscriber creates its
  subscription and dedupes on an ID in the event.

Done when every hop from step 1 points at a contract file that exists.

### 3. Scaffold the service

Under `samples-monorepo/<language>/<service>/`, with the layout the other samples use:

- one entry point that reads configuration from flags or environment
  variables with working local defaults, sets timeouts on every client and
  server, and shuts down gracefully on SIGTERM;
- a transport layer (HTTP routes or gRPC handlers) that maps domain errors
  to status codes;
- two or three domain packages with real branching logic;
- one port interface per outbound dependency, with an adapter implementing it;
- tests that run without any other service: domain rules table-driven, and
  each adapter against a local fake server;
- the build wrapper (`mvnw`, `gradlew`, etc.) and a `.gitignore` for build output;
- a `README.md` with contract, rules, layout, and run and test commands.

Done when the project builds and its tests pass.

### 4. Wire the existing services

For each inbound hop, change the calling project: add a client adapter behind
an interface defined where it is used, inject it in the entry point, add an
address flag, map the new failure modes to its own errors, and extend its
tests with a fake. For each outbound hop, check the callee already serves
what you need, and add it there if it does not.

When a later step of a request can fail after the new service changed state,
add a compensating call (as `orders` releases an inventory reservation when
pricing fails).

Done when every touched project builds and its tests pass.

### 5. Verify end to end

Start every service and drive the order flow with `curl`: one success per
customer tier and one request per new failure mode. Check the numbers
against the rules in the READMEs. Then index and trace:

```sh
dotweavier init samples-monorepo
dotweavier index samples-monorepo/<language>/<service>   # once per service
dotweavier trace path -A <caller> -B <callee>
```

dotweavier extracts Go only, so a trace reaches the Go client of a non-Go
service and stops there.

Done when every hop from step 1 has been exercised by a live request, and
every new Go interface hop shows up in a trace with the expected `sure` or
`guess` label.

### 6. Update the docs

Update `samples-monorepo/README.md` (services table, edge list, hop table, run and test
commands, response codes) and the README of every project you changed. Write
the expected trace output into the READMEs only as it was printed in step 5.

Done when each README command has been run and printed what the README says.

## Gotchas

- **gRPC from Go dials `127.0.0.1`**, not `localhost`. gRPC's DNS resolver can
  take 30 seconds to resolve `localhost`, which times out the first request.
- **Sure versus guess.** dotweavier marks a Go interface hop as sure only
  when it can see the concrete value reach the call: the entry point builds
  the adapter and passes it to a constructor. A value that travels through a
  `context.Context` is a guess (`notify.Notifier` is the sample of that).
- **Generated Go code is committed.** A Go consumer of a non-Go proto keeps a
  `buf.gen.yaml` whose `inputs` points at the owner's proto directory
  (`samples-monorepo/golang/orders/buf.gen.yaml`). Generate with `buf`,
  `protoc-gen-go` and `protoc-gen-go-grpc` on `PATH`; `go install` all three
  into a temporary `GOBIN` if they are missing.
- **JVM projects build for JDK 25**: Java with Maven (`maven.compiler.release`),
  Kotlin with Gradle toolchains. Run a built jar
  with a JDK 25 `java`, and confirm library versions against Maven Central
  rather than memory.
- **Spring Boot 4** splits its starters: `spring-boot-starter-webmvc`,
  `spring-boot-starter-restclient` and `spring-boot-starter-webmvc-test`
  (`MockMvc` is in `org.springframework.boot.webmvc.test.autoconfigure`).
  Snake_case on the wire is one property,
  `spring.jackson.property-naming-strategy=SNAKE_CASE`, shared by the
  controllers and the `RestClient.Builder` Spring provides.
- **Quarkus gRPC** runs on its own port (`quarkus.grpc.server.use-separate-server=true`),
  and Quarkus also opens an HTTP port; inventory takes `8092`. A gRPC handler
  that makes a blocking call is annotated `@Blocking`.
- **Pub/Sub runs locally** against `samples-monorepo/golang/tools/pubsub-emulator`
  through `PUBSUB_EMULATOR_HOST=localhost:8085`, which the Go client honours.
  For a client in another language, check its emulator docs; some need the
  endpoint set in code. Go tests use `pstest` in process.
- **Ports.** Take the next free port and record it in `samples-monorepo/README.md`.
