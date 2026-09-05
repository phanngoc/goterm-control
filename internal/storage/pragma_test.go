package storage

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// TestPragmasApplyToEveryPooledConnection guards the bug that made a shared
// database unsafe: pragmas used to be issued with conn.Exec after sql.Open,
// which binds them to whichever single pooled connection served the call.
// Connections the pool opened later ran with busy_timeout=0 and would return
// SQLITE_BUSY immediately instead of waiting out a concurrent writer.
func TestPragmasApplyToEveryPooledConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Hold several connections open at once so the pool is forced to create
	// more than one, then read the pragma back on each.
	const n = 4
	conns := make([]*sql.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	for i := 0; i < n; i++ {
		c, err := db.Conn().Conn(t.Context())
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns = append(conns, c)

		var timeout int
		if err := c.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatalf("read busy_timeout on conn %d: %v", i, err)
		}
		if timeout == 0 {
			t.Fatalf("conn %d has busy_timeout=0: writes will fail instantly under contention", i)
		}

		var fk int
		if err := c.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("read foreign_keys on conn %d: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d has foreign_keys=%d, want 1", i, fk)
		}
	}

	var mode string
	if err := db.Conn().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal (readers must not block the writer)", mode)
	}
}

// TestConcurrentWritersDoNotFail is the in-process half of the shared-database
// question: many goroutines writing through one pool must serialise on the
// busy timeout rather than error out.
func TestConcurrentWritersDoNotFail(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Conn().Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, who TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := db.Conn().Exec(`INSERT INTO t (who) VALUES (?)`, w); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}

	var count int
	if err := db.Conn().QueryRow(`SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := writers * each; count != want {
		t.Errorf("wrote %d rows, want %d", count, want)
	}
}
