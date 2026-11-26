package domain

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// TestTraceFieldGolden pins Go, Java and Kotlin field trace runs: both trees, plus call paths, hops and follow grafts for the indexed samples; -update rewrites it.
func TestTraceFieldGolden(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Progress: func(r IndexResult) {
		assert.NoError(t, r.Err, "index %s", r.Project)
	}}))
	type fieldCase struct {
		ws, addr string
		follow   int
	}
	var cases []fieldCase
	for _, c := range []struct{ ws, addr string }{
		{"../../cmd/dotweavier/testdata", "fieldproj:main.go:16"},
		{"../../cmd/dotweavier/testdata", "fieldproj:main.go:30"},
		{"testdata", "fieldcases:main.go:26"},
		{"testdata", "fieldcases:main.go:42"},
		{"testdata", "fieldcases:main.go:80"},
		{"testdata", "fieldcases:main.go:81"},
		{"../../samples/golang", "orders:internal/pricer/pricer.go:23"},
		{"../../samples/golang", "orders:internal/pricer/pricer.go:34"},
		{"../../samples/golang", "pricing:internal/server/server.go:47"},
		{"../../samples", "java/inventory:src/main/java/com/example/inventory/reservation/ReservationService.java:21"},
		{"../../samples", "java/inventory:src/main/java/com/example/inventory/reservation/ReservationService.java:24"},
		{"../../samples", "kotlin/shipping:src/main/kotlin/com/example/shipping/http/Routes.kt:47"},
		{"../../samples", "kotlin/shipping:src/main/kotlin/com/example/shipping/rates/RateCalculator.kt:13"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:9"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:12"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:23"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:46"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:50"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:4"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:7"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:17"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:30"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:53"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:55"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:22"},
		{"testdata", "jvmcases/java:src/main/java/cases/Cases.java:26"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:18"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:23"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:29"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:33"},
		{"testdata", "jvmcases/kotlin:src/main/kotlin/cases/Cases.kt:42"},
	} {
		cases = append(cases, fieldCase{c.ws, c.addr, 0})
	}
	// Hops followed: gRPC Go to Go, gRPC Go to Java, Pub/Sub Go to Go, HTTP
	// Kotlin to Go (into a handler registered as a method value).
	cases = append(cases,
		fieldCase{"../../samples/golang", "orders:internal/pricer/pricer.go:23", 2},
		fieldCase{"../../samples", "golang/orders:internal/inventory/client.go:26", 1},
		fieldCase{"../../samples", "golang/orders:internal/events/publisher.go:44", 1},
		fieldCase{"../../samples", "kotlin/shipping:src/main/kotlin/com/example/shipping/customers/CustomerDirectory.kt:26", 1},
	)
	for _, c := range cases {
		name := strings.NewReplacer("/", "_", ":", "_").Replace(c.addr)
		if c.follow > 0 {
			name += fmt.Sprintf("_follow%d", c.follow)
		}
		t.Run(name, func(t *testing.T) {
			r, err := TraceField(t.Context(), FieldQuery{Dir: c.ws, Follow: c.follow}, c.addr)
			require.NoError(t, err)
			for _, w := range r.Warnings {
				assert.NotContains(t, w, "hop not followed", "follow must succeed")
			}
			r.Warnings, r.Dir, r.Addr = nil, "", "" // timings and absolute paths; pinned by the warning tests instead
			got, err := json.MarshalIndent(r, "", " ")
			require.NoError(t, err)
			file := filepath.Join("testdata", "field_golden", name+".json")
			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
				require.NoError(t, os.WriteFile(file, got, 0o644))
				return
			}
			want, err := os.ReadFile(file)
			require.NoError(t, err)
			if !assert.Equal(t, string(want), string(got), "%s changed; diff it against %s.got (go test -run TestTraceFieldGolden -update rewrites it)", c.addr, file) {
				_ = os.WriteFile(file+".got", got, 0o644)
			}
		})
	}
}
