// Storage: one SQLite file per project under ~/.dotweavier (ADR 0004).
package domain

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// stored is one project's index: its own nodes, edges, gRPC client calls and
// handlers. Projects are joined on gRPC method names only at trace time, so
// re-indexing one project never touches another's file.
type stored struct {
	Project, Dir string
	Built        time.Time
	Graph        graph
}

// One SQLite file per project folder (ADR 0004), shared by every workspace that
// contains the folder. trace still loads whole graphs into memory; the tables
// just make them queryable without dotweavier.
func dotweavierDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotweavier")
}

// storePath is the index file of the project in the absolute folder dir.
func storePath(dir string) string {
	name := strings.ReplaceAll(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(dir)), "/"), "/", "__")
	return filepath.Join(storesDir(), name+".db")
}

func storesDir() string { return filepath.Join(dotweavierDir(), "projects") }

const schema = `
CREATE TABLE meta (project TEXT NOT NULL, dir TEXT NOT NULL, built TEXT NOT NULL);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, project TEXT NOT NULL, file TEXT NOT NULL, name TEXT NOT NULL,
	line INTEGER NOT NULL, end_line INTEGER NOT NULL, test INTEGER NOT NULL);
CREATE TABLE edges (src INTEGER NOT NULL REFERENCES nodes(id), dst INTEGER NOT NULL REFERENCES nodes(id),
	kind INTEGER NOT NULL, method TEXT NOT NULL);
CREATE TABLE grpc_calls (src INTEGER NOT NULL REFERENCES nodes(id), method TEXT NOT NULL, ext TEXT NOT NULL);
CREATE TABLE impls (method TEXT NOT NULL, node INTEGER NOT NULL REFERENCES nodes(id));
CREATE INDEX edges_src ON edges(src);
CREATE INDEX nodes_name ON nodes(name);
`

// save writes a fresh database next to the old one and renames it over, so a
// crash never leaves a half-written index.
func save(s *stored) error {
	if err := os.MkdirAll(storesDir(), 0o755); err != nil {
		return err
	}
	tmp := storePath(s.Dir) + ".tmp"
	os.Remove(tmp)
	if err := writeDB(tmp, s); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, storePath(s.Dir))
}

func writeDB(path string, s *stored) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, rows func(*sql.Stmt) error) error {
		st, err := tx.Prepare(q)
		if err != nil {
			return err
		}
		defer st.Close()
		return rows(st)
	}
	g := &s.Graph
	steps := []struct {
		q    string
		rows func(*sql.Stmt) error
	}{
		{`INSERT INTO meta VALUES (?, ?, ?)`, func(st *sql.Stmt) error {
			_, err := st.Exec(s.Project, s.Dir, s.Built.Format(time.RFC3339Nano))
			return err
		}},
		{`INSERT INTO nodes VALUES (?, ?, ?, ?, ?, ?, ?)`, func(st *sql.Stmt) error {
			for i, n := range g.Nodes {
				if _, err := st.Exec(i, n.Project, n.File, n.Name, n.Line, n.End, n.Test); err != nil {
					return err
				}
			}
			return nil
		}},
		{`INSERT INTO edges VALUES (?, ?, ?, ?)`, func(st *sql.Stmt) error {
			for from, es := range g.Out {
				for _, e := range es {
					if _, err := st.Exec(from, e.To, e.Kind, e.Method); err != nil {
						return err
					}
				}
			}
			return nil
		}},
		{`INSERT INTO grpc_calls VALUES (?, ?, ?)`, func(st *sql.Stmt) error {
			for _, c := range g.Calls {
				if _, err := st.Exec(c.From, c.Method, c.Ext); err != nil {
					return err
				}
			}
			return nil
		}},
		{`INSERT INTO impls VALUES (?, ?)`, func(st *sql.Stmt) error {
			for m, hs := range g.Impls {
				for _, h := range hs {
					if _, err := st.Exec(m, h); err != nil {
						return err
					}
				}
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := exec(s.q, s.rows); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func load(path string) (*stored, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	s := &stored{Graph: graph{ids: map[string]int32{}, Impls: map[string][]int32{}}}
	g := &s.Graph
	var built string
	if err := db.QueryRow(`SELECT project, dir, built FROM meta`).Scan(&s.Project, &s.Dir, &built); err != nil {
		return nil, err
	}
	if s.Built, err = time.Parse(time.RFC3339Nano, built); err != nil {
		return nil, err
	}
	// rowid order keeps node numbers and edge order exactly as indexed.
	each := func(q string, scan func(*sql.Rows) error) error {
		rows, err := db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := each(`SELECT project, file, name, line, end_line, test FROM nodes ORDER BY id`, func(r *sql.Rows) error {
		var n node
		err := r.Scan(&n.Project, &n.File, &n.Name, &n.Line, &n.End, &n.Test)
		g.Nodes = append(g.Nodes, n)
		return err
	}); err != nil {
		return nil, err
	}
	g.Out = make([][]edge, len(g.Nodes))
	if err := each(`SELECT src, dst, kind, method FROM edges ORDER BY rowid`, func(r *sql.Rows) error {
		var from int32
		var e edge
		if err := r.Scan(&from, &e.To, &e.Kind, &e.Method); err != nil {
			return err
		}
		g.Out[from] = append(g.Out[from], e)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT src, method, ext FROM grpc_calls ORDER BY rowid`, func(r *sql.Rows) error {
		var c grpcCall
		err := r.Scan(&c.From, &c.Method, &c.Ext)
		g.Calls = append(g.Calls, c)
		return err
	}); err != nil {
		return nil, err
	}
	if err := each(`SELECT method, node FROM impls ORDER BY rowid`, func(r *sql.Rows) error {
		var m string
		var h int32
		err := r.Scan(&m, &h)
		g.Impls[m] = append(g.Impls[m], h)
		return err
	}); err != nil {
		return nil, err
	}
	return s, nil
}

// readMeta reads only an index's meta row: its project name, folder and build time.
func readMeta(path string) (project, dir string, built time.Time, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer db.Close()
	var b string
	if err = db.QueryRow(`SELECT project, dir, built FROM meta`).Scan(&project, &dir, &b); err != nil {
		return "", "", time.Time{}, err
	}
	built, err = time.Parse(time.RFC3339Nano, b)
	return project, dir, built, err
}

// DefaultWorkspace is the workspace used when none is named.
const DefaultWorkspace = "default"

// The workspace registry. A workspace is a name with one or more registered
// folders; its projects are the ones detected under them. A project folder has
// one index, shared by every workspace that contains it; each workspace names
// the project by its path relative to the deepest of its folders containing it.
const registrySchema = `CREATE TABLE IF NOT EXISTS workspace_folders (
	workspace TEXT NOT NULL, dir TEXT NOT NULL, registered TEXT NOT NULL, PRIMARY KEY (workspace, dir))`

func openRegistry() (*sql.DB, error) {
	if err := os.MkdirAll(dotweavierDir(), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dotweavierDir(), "dotweavier.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(registrySchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateRegistry(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrateRegistry moves the folders of the old registry, where each workspace was
// one folder, into the default workspace.
func migrateRegistry(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'workspaces'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO workspace_folders (workspace, dir, registered)
		SELECT ?, dir, registered FROM workspaces; DROP TABLE workspaces`, DefaultWorkspace)
	return err
}

// Workspaces lists the names of the registered workspaces, sorted.
func Workspaces() ([]string, error) {
	return registryList(`SELECT DISTINCT workspace FROM workspace_folders ORDER BY workspace`)
}

// folders lists the registered folders of workspace ws, sorted; none if ws does not exist.
func folders(ws string) ([]string, error) {
	return registryList(`SELECT dir FROM workspace_folders WHERE workspace = ? ORDER BY dir`, ws)
}

func registryList(query string, args ...any) ([]string, error) {
	db, err := openRegistry()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// workspaceFolders is the registered folders of workspace ws (DefaultWorkspace for
// ""), or an error if it has none.
func workspaceFolders(ws string) (string, []string, error) {
	if ws == "" {
		ws = DefaultWorkspace
	}
	fs, err := folders(ws)
	if err == nil && len(fs) == 0 {
		err = fmt.Errorf("workspace %q does not exist; create it with: dotweavier index DIR --workspace %s", ws, ws)
	}
	return ws, fs, err
}

// register adds the absolute folder dir to workspace ws, creating ws if it does not exist.
func register(ws, dir string) error {
	db, err := openRegistry()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT OR IGNORE INTO workspace_folders (workspace, dir, registered) VALUES (?, ?, ?)`,
		ws, dir, time.Now().Format(time.RFC3339Nano))
	return err
}

// within reports whether the absolute folder dir is ws or inside it.
func within(dir, ws string) bool {
	return dir == ws || strings.HasPrefix(dir, ws+string(filepath.Separator))
}

// deepest is the deepest of folders that contains the absolute folder dir, or "".
func deepest(folders []string, dir string) string {
	best := ""
	for _, f := range folders {
		if within(dir, f) && len(f) > len(best) {
			best = f
		}
	}
	return best
}

// projectsIn detects the projects under folders, each named relative to the
// deepest folder that contains it. Two projects with one name are an error.
func projectsIn(folders []string) ([]project, error) {
	var out []project
	at := map[string]string{}
	for _, f := range folders {
		ps, err := discover(f)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if deepest(folders, p.dir) != f {
				continue // named by a deeper folder
			}
			if d, ok := at[p.Name]; ok {
				return nil, fmt.Errorf("two projects named %q: %s and %s", p.Name, d, p.dir)
			}
			at[p.Name] = p.dir
			out = append(out, p)
		}
	}
	return out, nil
}

// indexesIn maps the index file of every project folder inside folders to the
// project's name: its path relative to the deepest folder that contains it.
func indexesIn(folders []string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(storesDir(), "*.db"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range files {
		_, dir, _, err := readMeta(f)
		if err != nil {
			continue
		}
		top := deepest(folders, dir)
		if top == "" {
			continue
		}
		if rel, err := filepath.Rel(top, dir); err == nil {
			out[f] = filepath.ToSlash(rel)
		}
	}
	return out, nil
}

// loadIndexes merges the index of every project inside folders (absolute) into
// one graph, each project named as indexesIn names it; unreadable indexes are skipped.
func loadIndexes(folders []string) (*graph, map[string]bool, error) {
	files, err := indexesIn(folders)
	if err != nil {
		return nil, nil, err
	}
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	loaded := map[string]bool{}
	for f, name := range files {
		s, err := load(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip index %s: %v\n", f, err)
			continue
		}
		// The index carries the name it was built under; this workspace may name it differently.
		for i := range s.Graph.Nodes {
			if s.Graph.Nodes[i].Project != "" {
				s.Graph.Nodes[i].Project = name
			}
		}
		loaded[name] = true
		g.merge(&s.Graph)
	}
	return g, loaded, nil
}
