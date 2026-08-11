// Package store is the bot's persistent state: a SQLite-backed key-value
// store namespaced by (plugin, user). Plugins never share namespaces, and a
// user key of "" holds plugin-global state. Uses modernc.org/sqlite (pure Go)
// so cross-compiling for the Pi needs no cgo.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by Get for missing keys.
var ErrNotFound = errors.New("store: key not found")

// Store is the shared database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the bot database in dataDir.
func Open(dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, "owgbot.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc/sqlite is happiest with a single writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS kv (
			plugin TEXT NOT NULL,
			user   TEXT NOT NULL,
			key    TEXT NOT NULL,
			value  TEXT NOT NULL,
			PRIMARY KEY (plugin, user, key)
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Namespace returns a KV view scoped to one plugin.
func (s *Store) Namespace(plugin string) *KV {
	return &KV{db: s.db, plugin: plugin}
}

// KV is a plugin-scoped key-value store.
type KV struct {
	db     *sql.DB
	plugin string
}

// Get retrieves a value for (user, key). Returns ErrNotFound if absent.
func (k *KV) Get(user, key string) (string, error) {
	var v string
	err := k.db.QueryRow(`SELECT value FROM kv WHERE plugin=? AND user=? AND key=?`,
		k.plugin, user, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// Set stores a value for (user, key).
func (k *KV) Set(user, key, value string) error {
	_, err := k.db.Exec(`INSERT INTO kv (plugin,user,key,value) VALUES (?,?,?,?)
		ON CONFLICT(plugin,user,key) DO UPDATE SET value=excluded.value`,
		k.plugin, user, key, value)
	return err
}

// Delete removes (user, key). Deleting a missing key is not an error.
func (k *KV) Delete(user, key string) error {
	_, err := k.db.Exec(`DELETE FROM kv WHERE plugin=? AND user=? AND key=?`,
		k.plugin, user, key)
	return err
}

// Entry is one row from a List scan.
type Entry struct {
	User  string
	Key   string
	Value string
}

// List returns all entries whose key starts with prefix, across all users
// (pass user != "" via ListUser for a single user's entries).
func (k *KV) List(prefix string) ([]Entry, error) {
	rows, err := k.db.Query(`SELECT user, key, value FROM kv WHERE plugin=? AND key LIKE ? ESCAPE '\' ORDER BY user, key`,
		k.plugin, likePrefix(prefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ListUser returns one user's entries whose key starts with prefix.
func (k *KV) ListUser(user, prefix string) ([]Entry, error) {
	rows, err := k.db.Query(`SELECT user, key, value FROM kv WHERE plugin=? AND user=? AND key LIKE ? ESCAPE '\' ORDER BY key`,
		k.plugin, user, likePrefix(prefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.User, &e.Key, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// likePrefix escapes LIKE wildcards in prefix and appends %.
func likePrefix(prefix string) string {
	escaped := make([]byte, 0, len(prefix)+4)
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c == '%' || c == '_' || c == '\\' {
			escaped = append(escaped, '\\')
		}
		escaped = append(escaped, c)
	}
	return string(escaped) + "%"
}
