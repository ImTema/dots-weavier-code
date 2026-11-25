package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A trace started from inside one project's folder must still see the indexed
// projects next to it: the run traces the registered folder that holds the
// project, where projects carry the names their indexes have.
func TestTraceFieldFromProjectFolderFindsHops(t *testing.T) {
	isolate(t)
	_, err := InitWorkspace("../../samples", "")
	require.NoError(t, err)
	require.NoError(t, Index(t.Context(), IndexOptions{Names: []string{"golang/orders", "golang/pricing"}}))

	r, err := TraceField(t.Context(), FieldQuery{Dir: "../../samples/golang/orders"}, "internal/pricer/pricer.go:23")
	require.NoError(t, err)
	assert.Equal(t, "golang/orders:internal/pricer/pricer.go:23", r.Addr, "want the address as the workspace folder names it")
	assert.Contains(t, hopsIn(r.Uses), "grpc /pricing.v1.PricingService/Quote -> golang/pricing", "warnings %q", r.Warnings)
}

// hopsIn lists every hop under ss as "kind method -> project".
func hopsIn(ss []*FieldStep) []string {
	var out []string
	for _, s := range ss {
		for _, h := range s.Hops {
			out = append(out, h.Kind+" "+h.Method+" -> "+h.Project)
		}
		out = append(out, hopsIn(s.Kids)...)
	}
	return out
}
