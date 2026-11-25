package domain

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// Source reads the file at [project:]rel/path[:LINE] in a project under ws; a path leaving the project is an error.
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

// errNoProject: no project has the file an address names.
var errNoProject = errors.New("no project has")

// resolveFile finds the project and absolute path of [project:]rel/path.go.
func resolveFile(projects []project, s string) (project, string, error) {
	name, file, ok := strings.Cut(s, ":")
	if !ok {
		name, file = "", s
	}
	var hits []project
	for _, p := range projects {
		if name != "" && p.Name != name {
			continue
		}
		if _, err := os.Stat(filepath.Join(p.dir, file)); err == nil {
			hits = append(hits, p)
		}
	}
	if abs, err := filepath.Abs(file); name == "" && len(hits) == 0 && err == nil {
		if _, err := os.Stat(abs); err == nil { // absolute or cwd-relative: innermost project wins
			for _, p := range projects {
				if strings.HasPrefix(abs, p.dir+string(filepath.Separator)) {
					hits = append(hits, p)
				}
			}
			slices.SortFunc(hits, func(a, b project) int { return cmp.Compare(len(b.dir), len(a.dir)) })
			if len(hits) > 0 {
				return hits[0], abs, nil
			}
		}
	}
	switch len(hits) {
	case 0:
		return project{}, "", fmt.Errorf("%w %s", errNoProject, s)
	case 1:
		return hits[0], filepath.Join(hits[0].dir, file), nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return project{}, "", fmt.Errorf("%s is in several projects (%s); prefix it with project:", s, strings.Join(names, ", "))
}
