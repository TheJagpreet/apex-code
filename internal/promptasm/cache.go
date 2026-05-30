package promptasm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteCache struct {
	db *sql.DB
}

func OpenSQLiteCache(path string) (*SQLiteCache, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	c := &SQLiteCache{db: db}
	if err := c.Init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func OpenMemoryCache() (*SQLiteCache, error) {
	return OpenSQLiteCache(":memory:")
}

func (c *SQLiteCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *SQLiteCache) Init(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `create table if not exists cache_entries (
		key text primary key,
		kind text not null,
		value text not null,
		hash text not null,
		created_at integer not null
	)`)
	return err
}

func (c *SQLiteCache) Get(ctx context.Context, key string) (CacheEntry, bool, error) {
	var entry CacheEntry
	err := c.db.QueryRowContext(ctx, `select key, kind, value, hash, created_at from cache_entries where key = ?`, key).
		Scan(&entry.Key, &entry.Kind, &entry.Value, &entry.Hash, &entry.CreatedAt)
	if err == sql.ErrNoRows {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, err
	}
	return entry, true, nil
}

func (c *SQLiteCache) Put(ctx context.Context, entry CacheEntry) error {
	if entry.Hash == "" {
		entry.Hash = HashText(entry.Value)
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	_, err := c.db.ExecContext(ctx, `insert into cache_entries(key, kind, value, hash, created_at)
		values(?, ?, ?, ?, ?)
		on conflict(key) do update set kind=excluded.kind, value=excluded.value, hash=excluded.hash, created_at=excluded.created_at`,
		entry.Key, entry.Kind, entry.Value, entry.Hash, entry.CreatedAt)
	return err
}

func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func CacheKey(kind, body string) string {
	return kind + ":" + HashText(body)
}

var _ CacheStore = (*SQLiteCache)(nil)
