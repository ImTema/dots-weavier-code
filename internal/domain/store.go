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

// One SQLite file per project (ADR 0004). trace still loads whole graphs into
// memory; the tables just make them queryable without dotweavier.
func storeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotweavier", "proto")
}

func storePath(project string) string {
	return filepath.Join(storeDir(), strings.ReplaceAll(project, "/", "__")+".db")
}

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
	if err := os.MkdirAll(storeDir(), 0o755); err != nil {
		return err
	}
	tmp := storePath(s.Project) + ".tmp"
	os.Remove(tmp)
	if err := writeDB(tmp, s); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, storePath(s.Project))
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

// loadIndexes merges every project's store into one graph; unreadable stores are skipped.
func loadIndexes() (*graph, map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(storeDir(), "*.db"))
	if err != nil {
		return nil, nil, err
	}
	g := &graph{ids: map[string]int32{}, Impls: map[string][]int32{}}
	loaded := map[string]bool{}
	for _, f := range files {
		s, err := load(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip index %s: %v\n", f, err)
			continue
		}
		loaded[s.Project] = true
		g.merge(&s.Graph)
	}
	return g, loaded, nil
}
