package deploy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// ProdStatus is PROD as Cloudflare reports it.
type ProdStatus struct {
	Provider  string `json:"provider,omitempty"` // deploy.target
	Title     string `json:"title,omitempty"`    // "Cloudflare"
	Declared  bool   `json:"declared"`
	Connected bool   `json:"connected"` // credentials present
	Error     string `json:"error,omitempty"`
	Name      string `json:"name,omitempty"`
	Deployed  bool   `json:"deployed"`
	// DeployedAt is when the live version went out.
	DeployedAt string `json:"deployed_at,omitempty"`
	// URL is where it answers: the custom domain when one is attached, else
	// workers.dev.
	URL        string   `json:"url,omitempty"`
	WorkersDev string   `json:"workers_dev,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	// WantDomain is the domain bomclaw.json asks for; it shows as pending
	// until a deploy attaches it.
	WantDomain string    `json:"want_domain,omitempty"`
	Database   *DBStatus `json:"database,omitempty"`
}

// DBStatus is one environment's database.
type DBStatus struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`   // file (DEV) or D1 name (PROD)
	Exists bool   `json:"exists"` // created / present
	Bytes  int64  `json:"bytes"`
	Tables int    `json:"tables"`
	Error  string `json:"error,omitempty"`
}

// DevStatus is the local half: what a deploy would upload.
type DevStatus struct {
	Declared  bool      `json:"declared"`
	StaticDir string    `json:"static_dir,omitempty"`
	Built     bool      `json:"built"` // the static folder exists and is not empty
	BuiltAt   string    `json:"built_at,omitempty"`
	Files     int       `json:"files"`
	Database  *DBStatus `json:"database,omitempty"`
}

// Dev inspects the local build and database.
func Dev(r Resolved) DevStatus {
	st := DevStatus{Declared: true, StaticDir: r.StaticDir}
	if r.StaticDir != "" {
		var newest time.Time
		n := 0
		_ = walkFiles(r.StaticDir, func(info os.FileInfo) {
			n++
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		})
		st.Files = n
		st.Built = n > 0
		if n > 0 {
			st.BuiltAt = newest.Format(time.RFC3339)
		}
	}
	if r.Database != nil {
		st.Database = LocalDB(r.DBFile)
	}
	return st
}

// LocalDB describes a SQLite file without changing it.
func LocalDB(path string) *DBStatus {
	st := &DBStatus{Engine: "sqlite", Name: path}
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			st.Error = err.Error()
		}
		return st
	}
	st.Exists, st.Bytes = true, info.Size()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		st.Error = err.Error()
		return st
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&st.Tables); err != nil {
		st.Error = err.Error()
	}
	return st
}

// Prod asks Cloudflare what is live.
func Prod(ctx context.Context, c *Client, r Resolved) ProdStatus {
	st := ProdStatus{Declared: true, Name: r.Name, WantDomain: r.Domain, Connected: c != nil && c.Creds.OK()}
	if !st.Connected {
		st.Error = ErrNoCreds.Error()
		return st
	}
	dep, err := c.LatestDeployment(ctx, r.Name)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if dep != nil {
		st.Deployed = true
		st.DeployedAt = dep.CreatedOn.Format(time.RFC3339)
		if sub, err := c.Subdomain(ctx); err == nil && sub != "" {
			st.WorkersDev = fmt.Sprintf("https://%s.%s.workers.dev", r.Name, sub)
			st.URL = st.WorkersDev
		}
		if hosts, err := c.Domains(ctx, r.Name); err == nil {
			st.Domains = hosts
			if len(hosts) > 0 {
				st.URL = "https://" + hosts[0]
			}
		}
	}
	if r.Database != nil {
		db := &DBStatus{Engine: "d1", Name: r.DBName}
		if d, err := c.FindD1(ctx, r.DBName); err != nil {
			db.Error = err.Error()
		} else if d != nil {
			db.Exists, db.Bytes, db.Tables = true, d.FileSize, d.NumTables
		}
		st.Database = db
	}
	return st
}

func walkFiles(dir string, fn func(os.FileInfo)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := dir + string(os.PathSeparator) + e.Name()
		if e.IsDir() {
			_ = walkFiles(p, fn)
			continue
		}
		if info, err := e.Info(); err == nil {
			fn(info)
		}
	}
	return nil
}
