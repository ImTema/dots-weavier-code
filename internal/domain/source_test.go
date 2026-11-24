package domain

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceReadsProjectFilesOnly(t *testing.T) {
	ws, err := filepath.Abs("../../samples")
	require.NoError(t, err)
	f, err := Source(ws, "golang/orders:internal/httpapi/httpapi.go:45")
	require.NoError(t, err)
	assert.Equal(t, "golang/orders", f.Project)
	assert.Equal(t, "internal/httpapi/httpapi.go", f.File)
	assert.Equal(t, 45, f.Line)
	assert.Contains(t, f.Text, "func (h *Handler) placeOrder")
	for _, bad := range []string{"golang/orders:../pricing/go.mod", "golang/orders:../../../go.mod", "/etc/hosts"} {
		_, err := Source(ws, bad)
		assert.Error(t, err, "Source(%q) served a file outside the project", bad)
	}
}
