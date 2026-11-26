package domain

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// errNoProject: no project has the file an address names.
var errNoProject = errors.New("no project has")

// locator resolves symbol addresses to projects on disk over the registered folders of workspace ws; the zero value works.
type locator struct {
	ws      string               // DefaultWorkspace for ""
	folders []string             // ws's registered folders, absolute, once loaded
	loaded  bool                 // folders is loaded
	found   map[string][]project // projects under each folder, named relative to it, every language
}

// spot is an address resolved on disk.
type spot struct {
	Folder   string    // absolute; Projects are named relative to it
	Projects []project // the projects under Folder in the file's language; every language for an unknown extension or no file
	Project  project   // the project the address names, one of Projects
	File     string    // absolute path of the file; "" for project::Name
	Addr     address   // canonical: Project.Name, File relative to it, Name and Line as given
}

// locate resolves any address form under dir first (if not ""), else in the workspace under any folder's name; Folder is the deepest workspace folder holding the project, else dir.
func (l *locator) locate(dir, s string) (spot, error) {
	a, err := parseAddr(s)
	if err != nil {
		return spot{}, err
	}
	fs, err := l.wsFolders()
	if err != nil {
		return spot{}, err
	}
	if dir != "" {
		if dir, err = filepath.Abs(dir); err != nil {
			return spot{}, err
		}
		ps, err := l.under(dir, a)
		if err != nil {
			return spot{}, err
		}
		p, file, err := match(ps, a)
		if err == nil {
			if top := deepest(fs, p.dir); top != "" {
				if sp, ok := l.at(top, p, file, a); ok {
					return sp, nil
				}
			}
			sp, _ := l.at(dir, p, file, a)
			return sp, nil
		}
		if !errors.Is(err, errNoProject) {
			return spot{}, err
		}
	}
	var ps []project
	for _, f := range fs {
		in, err := l.under(f, a)
		if err != nil {
			continue // a registered folder that was moved or deleted
		}
		ps = append(ps, in...)
	}
	p, file, err := match(ps, a)
	if errors.Is(err, errNoProject) {
		return spot{}, fmt.Errorf("%w in workspace %s", err, cmp.Or(l.ws, DefaultWorkspace))
	}
	if err != nil {
		return spot{}, err
	}
	sp, _ := l.at(deepest(fs, p.dir), p, file, a)
	return sp, nil
}

// in resolves address s among the projects under folder, named relative to it, as a run names its steps and hops.
func (l *locator) in(folder, s string) (spot, error) {
	a, err := parseAddr(s)
	if err != nil {
		return spot{}, err
	}
	if folder, err = filepath.Abs(folder); err != nil {
		return spot{}, err
	}
	ps, err := l.under(folder, a)
	if err != nil {
		return spot{}, err
	}
	p, file, err := match(ps, a)
	if err != nil {
		return spot{}, err
	}
	sp, _ := l.at(folder, p, file, a)
	return sp, nil
}

// legacyRunFolder is the folder of the first workspace that locates addr, for runs saved before runs kept their folder.
func legacyRunFolder(addr string) (string, error) {
	all, err := Workspaces()
	if err != nil {
		return "", err
	}
	for _, ws := range all {
		l := locator{ws: ws}
		if sp, err := l.locate("", addr); err == nil {
			return sp.Folder, nil
		}
	}
	return "", fmt.Errorf("%w %s in any workspace", errNoProject, addr)
}

// wsFolders is the registered folders of the workspace, read once; none if it does not exist.
func (l *locator) wsFolders() ([]string, error) {
	if !l.loaded {
		fs, err := folders(cmp.Or(l.ws, DefaultWorkspace))
		if err != nil {
			return nil, err
		}
		l.folders, l.loaded = fs, true
	}
	return l.folders, nil
}

// under is the projects under folder in the language of a's file, discovering folder once.
func (l *locator) under(folder string, a address) ([]project, error) {
	ps, ok := l.found[folder]
	if !ok {
		var err error
		if ps, err = discover(folder); err != nil {
			return nil, err
		}
		if l.found == nil {
			l.found = map[string][]project{}
		}
		l.found[folder] = ps
	}
	lang := langOfExt(a.File)
	return slices.DeleteFunc(slices.Clone(ps), func(p project) bool { return lang != "" && p.lang != lang }), nil
}

// at is the spot of file in project p seen from folder; false if folder's discovery does not reach p.
func (l *locator) at(folder string, p project, file string, a address) (spot, bool) {
	ps, err := l.under(folder, a)
	i := slices.IndexFunc(ps, func(q project) bool { return q.dir == p.dir })
	if err != nil || i < 0 {
		return spot{}, false
	}
	sp := spot{Folder: folder, Projects: ps, Project: ps[i], Addr: a}
	sp.Addr.Project, sp.Addr.File = ps[i].Name, ""
	if a.File != "" {
		rel, err := filepath.Rel(p.dir, file)
		if err != nil {
			return spot{}, false
		}
		sp.File, sp.Addr.File = file, filepath.ToSlash(rel)
	}
	return sp, true
}

// match finds the project and absolute file a names among ps; a bare file is relative to any project, else absolute or cwd-relative in the innermost project.
func match(ps []project, a address) (project, string, error) {
	var hits []project
	for _, p := range ps {
		if a.Project != "" && p.Name != a.Project || slices.ContainsFunc(hits, func(h project) bool { return h.dir == p.dir }) {
			continue
		}
		if _, err := os.Stat(filepath.Join(p.dir, a.File)); err == nil {
			hits = append(hits, p)
		}
	}
	if abs, err := filepath.Abs(a.File); a.Project == "" && len(hits) == 0 && err == nil {
		if _, err := os.Stat(abs); err == nil {
			for _, p := range ps {
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
		return project{}, "", fmt.Errorf("%w %s", errNoProject, a)
	case 1:
		return hits[0], filepath.Join(hits[0].dir, a.File), nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return project{}, "", fmt.Errorf("%s is in several projects (%s); prefix it with project:", a.loc(), strings.Join(names, ", "))
}
