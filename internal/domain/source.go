package domain

import (
	"fmt"
	"os"
	"path/filepath"
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

// Source reads the file at [project:]rel/path[:LINE] in a project of workspace ws (DefaultWorkspace for ""); a path leaving the project is an error.
func Source(ws, loc string) (*SourceFile, error) {
	l := locator{ws: ws}
	sp, err := l.locate("", loc)
	if err != nil {
		return nil, err
	}
	a, p, path, rel := sp.Addr, sp.Project, sp.File, filepath.FromSlash(sp.Addr.File)
	if path == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
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
	return &SourceFile{Project: p.Name, File: a.File, Line: a.Line, Text: string(b)}, nil
}
