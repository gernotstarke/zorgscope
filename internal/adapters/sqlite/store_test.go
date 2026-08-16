package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/ports/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.Store {
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path) // second open must not fail or re-run migrations
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := s.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	_ = s.Close()
}
