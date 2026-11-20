package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SourceFile is one whole source file of a project, for viewing.
type SourceFile struct {
	Project string `json:"project"`
	File    string `json:"file"` // relative to the project
	Line    int    `json:"line"` // the line asked for, 0 if none
	Text    string `json:"text"`
}

// maxSource caps what Source returns; generated files can be huge.
const maxSource = 2 << 20

// Source reads the file at [project:]rel/path[:LINE] in a project under ws. Only
// files inside a project are served: a path that leaves it (../) is an error.
func Source(ws, loc string) (*SourceFile, error) {
	line := 0
	if i := strings.LastIndex(loc, ":"); i >= 0 {
		if n, err := strconv.Atoi(loc[i+1:]); err == nil {
			loc, line = loc[:i], n
		}
	}
	projects, err := discover(ws)
	if err != nil {
		return nil, err
	}
	p, path, err := resolveFile(projects, loc)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(p.dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("%s is outside project %s", loc, p.Name)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxSource {
		return nil, fmt.Errorf("%s is not a regular file under %d MB", loc, maxSource>>20)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &SourceFile{Project: p.Name, File: filepath.ToSlash(rel), Line: line, Text: string(b)}, nil
}
