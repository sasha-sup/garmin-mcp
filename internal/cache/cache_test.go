package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPutGetAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c")
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("sleep/2026-01-01", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	data, _, ok := c.Get("sleep/2026-01-01")
	if !ok || string(data) != `{"a":1}` {
		t.Fatalf("Get = %q, %v", data, ok)
	}
	for _, p := range []string{dir, filepath.Join(dir, version), filepath.Join(dir, version, "sleep")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", p, st.Mode().Perm())
		}
	}
	st, err := os.Stat(filepath.Join(dir, version, "sleep", "2026-01-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestNewTightensExistingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", st.Mode().Perm())
	}
}

func TestRejectsBadKeys(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"", "../x", "a/../../b", "/abs", "a//b", "A", "a/b/", "a.b"} {
		if err := c.Put(k, []byte("x")); err == nil {
			t.Errorf("Put(%q) succeeded", k)
		}
		if _, _, ok := c.Get(k); ok {
			t.Errorf("Get(%q) succeeded", k)
		}
	}
}

func TestNilCacheIsNoop(t *testing.T) {
	var c *Cache
	if err := c.Put("a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Get("a"); ok {
		t.Fatal("nil cache returned data")
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
}

func TestClear(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Put("a/b", []byte("x"))
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Get("a/b"); ok {
		t.Fatal("entry survived Clear")
	}
}
