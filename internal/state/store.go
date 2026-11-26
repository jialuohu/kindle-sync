// Package state stores durable workflow records for one library and one Kindle.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"kindle-sync/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	lock *os.File
}

// Open acquires the writer lock before any network side effects. Read-only
// commands can inspect an existing state directory or preview an empty one.
func Open(dir, identity string, writable bool) (s *Store, err error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("state_dir must be absolute")
	}
	s = &Store{}
	opened := s
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	path := filepath.Join(dir, "sync.sqlite")
	dsn := ":memory:"
	if writable {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		s.lock, err = os.OpenFile(filepath.Join(dir, "sync.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return nil, fmt.Errorf("another sync owns the state lock: %w", err)
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	if _, e := os.Stat(path); e == nil {
		u := url.URL{Scheme: "file", Path: path}
		q := u.Query()
		if writable {
			q.Set("mode", "rw")
		} else {
			q.Set("mode", "ro")
		}
		q.Set("_pragma", "busy_timeout(5000)")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	s.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	if writable || dsn == ":memory:" {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS receipts (id TEXT NOT NULL, version TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(id,version));
INSERT OR IGNORE INTO meta(k,v) VALUES('identity',?)`, identity)
		if err != nil {
			return nil, err
		}
	}
	var actual string
	if err = s.db.QueryRow("SELECT v FROM meta WHERE k='identity'").Scan(&actual); err != nil {
		return nil, err
	}
	if actual != identity {
		return nil, errors.New("state directory belongs to a different library, collection, Kindle, or Drive folder; use its original configuration or a separate state_dir")
	}
	return s, nil
}
func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
	if s.lock != nil {
		s.lock.Close()
	}
}
func (s *Store) Initialized() (bool, error) {
	var yes bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM meta WHERE k='baseline')").Scan(&yes)
	return yes, err
}
func (s *Store) Seed(papers []model.Paper) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO meta(k,v) VALUES('baseline','1')"); err != nil {
		return err
	}
	for _, p := range papers {
		data, err := json.Marshal(model.Job{Paper: p, State: "baseline"})
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO jobs(id,data) VALUES(?,?)", p.Key, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) PutJob(j model.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO jobs(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", j.Key, string(data))
	return err
}
func (s *Store) PutReceipt(r model.Receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO receipts(id,version,data) VALUES(?,?,?) ON CONFLICT(id,version) DO UPDATE SET data=excluded.data", r.ID, r.Version, string(data))
	return err
}
func records[T any](db *sql.DB, query string) ([]T, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		var b []byte
		var v T
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Jobs() ([]model.Job, error) {
	return records[model.Job](s.db, "SELECT data FROM jobs ORDER BY id")
}
func (s *Store) Receipts() ([]model.Receipt, error) {
	return records[model.Receipt](s.db, "SELECT data FROM receipts ORDER BY id,version")
}

// BindFolder allows a pilot submission before the Scribe creates its export
// folder. Once linked, another folder needs an independently scoped state DB.
func (s *Store) BindFolder(folder string, writable bool) error {
	var saved string
	err := s.db.QueryRow("SELECT v FROM meta WHERE k='drive_folder'").Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		if folder != "" && writable {
			_, err = s.db.Exec("INSERT INTO meta(k,v) VALUES('drive_folder',?)", folder)
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if saved != folder {
		return errors.New("configured Drive folder differs from the bound folder; restore its ID or use a separate state_dir")
	}
	return nil
}
