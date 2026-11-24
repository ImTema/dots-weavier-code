package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// TestTraceFieldGolden pins both trees of Go, Java and Kotlin field traces, so
// refactors of field*.go and jvm_field.go can be checked byte for byte. Call paths
// and hops come from the user's index and are left out. Run with -update after an
// intended change.
func TestTraceFieldGolden(t *testing.T) {
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
		name := strings.NewReplacer("/", "_", ":", "_").Replace(c.addr)
		t.Run(name, func(t *testing.T) {
			r, err := TraceField(context.Background(), FieldQuery{Dir: c.ws}, c.addr)
			if err != nil {
				t.Fatal(err)
			}
			strip(r.Origin)
			strip(r.Uses)
			r.Warnings = nil // timings and absolute paths; pinned by the warning tests instead
			got, err := json.MarshalIndent(r, "", " ")
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join("testdata", "field_golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s changed; diff it against %s (go test -run TestTraceFieldGolden -update rewrites it)", c.addr, file)
				_ = os.WriteFile(file+".got", got, 0o644)
			}
		})
	}
}

func strip(ss []*FieldStep) {
	for _, s := range ss {
		s.Call, s.Hops = nil, nil
		strip(s.Kids)
	}
}
