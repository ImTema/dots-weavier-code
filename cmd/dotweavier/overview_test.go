package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dotweavier/internal/domain"
)

// The field page's routes: a trace keeps the options the request asked for,
// and the kept run lists, reads back and exports.
func TestFieldRoutes(t *testing.T) {
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output() // a new HOME would move them
	require.NoError(t, err)
	for i, v := range strings.Fields(string(out)) {
		t.Setenv([]string{"GOPATH", "GOMODCACHE", "GOCACHE"}[i], v)
	}
	t.Setenv("HOME", t.TempDir())
	ws, err := filepath.Abs("testdata")
	require.NoError(t, err)
	_, err = domain.InitWorkspace(ws, "")
	require.NoError(t, err)
	src, err := domain.LoadOverview(domain.DefaultWorkspace)
	require.NoError(t, err)
	mux := http.NewServeMux()
	fieldRoutes(mux, func(http.ResponseWriter) *domain.OverviewSource { return src })
	do := func(method, url string, guard bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, nil)
		if guard {
			req.Header.Set("X-Dotweavier", "1")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	assert.Equal(t, http.StatusForbidden, do("POST", "/trace-field?addr=fieldproj:main.go:16", false).Code)
	w := do("POST", "/trace-field?addr=fieldproj:main.go:16&max-projects=1&only=origin&include-tests=1", true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var traced struct{ Run string }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &traced))

	w = do("GET", "/trace-field/run?run="+traced.Run, false)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var run domain.FieldRun
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &run))
	assert.Equal(t, traced.Run, run.Name)
	assert.Equal(t, "origin", run.Only)
	assert.Equal(t, 1, run.MaxProjects)
	assert.True(t, run.IncludeTests)
	assert.NotEmpty(t, run.Uses, "the run keeps the tree --only hides")

	w = do("GET", "/trace-field/runs", false)
	require.Equal(t, http.StatusOK, w.Code)
	var runs []domain.FieldRunInfo
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &runs))
	require.Len(t, runs, 1)
	assert.Equal(t, traced.Run, runs[0].Name)

	w = do("GET", "/trace-field/export?run="+traced.Run+"&open=3", false)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "window.OPEN = 3")
	assert.NotContains(t, w.Body.String(), ws, "export leaks the local folder")

	assert.Equal(t, http.StatusNotFound, do("GET", "/trace-field/run?run=../x", false).Code)
	w = do("POST", "/trace-field/follow?run="+traced.Run, true)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "no hop left to follow")
}
