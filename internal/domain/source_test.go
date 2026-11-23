package domain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceReadsProjectFilesOnly(t *testing.T) {
	ws, err := filepath.Abs("../../samples")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Source(ws, "golang/orders:internal/httpapi/httpapi.go:45")
	if err != nil {
		t.Fatal(err)
	}
	if f.Project != "golang/orders" || f.File != "internal/httpapi/httpapi.go" || f.Line != 45 || !strings.Contains(f.Text, "func (h *Handler) placeOrder") {
		t.Errorf("got %s:%s:%d, %d bytes", f.Project, f.File, f.Line, len(f.Text))
	}
	for _, bad := range []string{"golang/orders:../pricing/go.mod", "golang/orders:../../../go.mod", "/etc/hosts"} {
		if _, err := Source(ws, bad); err == nil {
			t.Errorf("Source(%q) served a file outside the project", bad)
		}
	}
}
