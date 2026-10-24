// Package store owns sentient's SQLite database: schema, migrations and the
// write paths used by sentientd. Only sentientd opens the database; everything
// else goes through the local API.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

// DBFileName is the database file inside the data directory.
const DBFileName = "sentient.db"

// Store is an open sentient database.
type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the database in dataDir, enables WAL mode
// and applies any pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return OpenPath(ctx, filepath.Join(dataDir, DBFileName))
}

// OpenPath opens the database file at path.
func OpenPath(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// Every transaction we begin writes. Take the write lock at BEGIN: a
	// deferred transaction that reads and then writes is refused at once with
	// SQLITE_BUSY if another writer committed in between (the single
	// connection below prevents that inside the daemon; this covers another
	// process holding the database open).
	q.Add("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection: the ingester, the kernel collector, the API and the
	// monitor all share it, so their reads and writes queue in order instead
	// of colliding. Never run a query on Store.DB while a Tx or an open Rows is
	// held in the same goroutine: it would wait for the connection forever.
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// The file holds raw agent transcripts: keep it private.
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// ChainHash computes a raw record's hash: it covers the previous record's
// hash, so editing or deleting any earlier record breaks the chain.
func ChainHash(prevHash, ts, sourceFile, record string) string {
	h := sha256.New()
	for _, part := range []string{prevHash, ts, sourceFile, record} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
