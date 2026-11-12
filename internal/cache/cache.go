// Package cache stores Garmin API responses on disk.
//
// The data is personal health data, so the cache directory is created with
// mode 0700 and every file with mode 0600. Writes are atomic (temp file plus
// rename), so a crash never leaves a half-written entry.
package cache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// version is part of every path. Bump it when the stored format changes so
// old entries are ignored instead of misread.
const version = "v1"

var keyRE = regexp.MustCompile(`^[a-z0-9_-]+(/[a-z0-9_-]+)*$`)

// Cache is a directory of cached responses. A nil *Cache is valid and
// behaves as an always-empty cache that discards writes.
type Cache struct {
	dir string
}

// maxEntryBytes bounds what Get reads, matching the client's response limit.
const maxEntryBytes = 32 << 20

// New creates (or reuses) the cache directory. New directories get mode 0700.
// Only the versioned subdirectory the cache owns is chmod-ed, so pointing
// GARMIN_CACHE_DIR at an existing directory never changes its mode.
func New(dir string) (*Cache, error) {
	root := filepath.Join(dir, version)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	// MkdirAll does not change the mode of a directory that already exists.
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // directories need the x bit; 0700 is owner-only
		return nil, fmt.Errorf("restrict cache dir permissions: %w", err)
	}
	return &Cache{dir: root}, nil
}

// Dir returns the directory that holds the entries, or "" for a nil cache.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

func (c *Cache) path(key string) (string, error) {
	if !keyRE.MatchString(key) {
		return "", fmt.Errorf("invalid cache key %q", key)
	}
	return filepath.Join(c.dir, filepath.FromSlash(key)+".json"), nil
}

// Get returns the entry for key and the time it was written.
func (c *Cache) Get(key string) (data []byte, written time.Time, ok bool) {
	if c == nil {
		return nil, time.Time{}, false
	}
	p, err := c.path(key)
	if err != nil {
		return nil, time.Time{}, false
	}
	// Refuse symlinks and special files; Open below would follow a symlink.
	if st, err := os.Lstat(p); err != nil || !st.Mode().IsRegular() {
		return nil, time.Time{}, false
	}
	f, err := os.Open(p) //nolint:gosec // p is built from a key validated by keyRE
	if err != nil {
		return nil, time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, false
	}
	data, err = io.ReadAll(io.LimitReader(f, maxEntryBytes+1))
	if err != nil || len(data) > maxEntryBytes {
		return nil, time.Time{}, false
	}
	return data, st.ModTime(), true
}

// Put stores data under key, replacing any previous entry.
func (c *Cache) Put(key string, data []byte) error {
	if c == nil {
		return nil
	}
	p, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create cache subdir: %w", err)
	}
	// CreateTemp creates the file with mode 0600.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create cache temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write cache entry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write cache entry: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("commit cache entry: %w", err)
	}
	return nil
}

// Clear deletes every entry.
func (c *Cache) Clear() error {
	if c == nil {
		return nil
	}
	if err := os.RemoveAll(c.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear cache: %w", err)
	}
	return os.MkdirAll(c.dir, 0o700)
}
