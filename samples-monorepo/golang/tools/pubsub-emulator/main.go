// Command pubsub-emulator serves an in-memory Pub/Sub API for running the
// samples locally. Point clients at it with PUBSUB_EMULATOR_HOST=localhost:8085.
//
// It wraps pstest, the fake the Go client library ships for tests. State is
// lost on exit. The official emulator (gcloud beta emulators pubsub start)
// works the same way if you have the gcloud CLI.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cloud.google.com/go/pubsub/v2/pstest"
)

func main() {
	port := flag.Int("port", 8085, "listen port")
	flag.Parse()

	srv := pstest.NewServerWithPort(*port)
	slog.Info("pubsub emulator listening", "addr", srv.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := srv.Close(); err != nil {
		slog.Error("close", "err", err)
	}
}
