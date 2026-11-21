package domain

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/field_golden/*.json")

// TestTraceFieldGolden pins both trees of Go field traces, so refactors of field.go
// can be checked byte for byte. Call paths and hops come from the user's index and
// are left out. Run with -update after an intended change.
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
	} {
		name := strings.NewReplacer("/", "_", ":", "_").Replace(c.addr)
		t.Run(name, func(t *testing.T) {
			r, err := TraceField(c.ws, c.addr, false, 10)
			if err != nil {
				t.Fatal(err)
			}
			strip(r.Origin)
			strip(r.Uses)
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
