package index

import (
	"context"
	"testing"
)

func TestPragmasAppliedOnAllPooledConnections(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir + "/q.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Force several pooled connections and check per-connection pragmas on each.
	conns, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conns.Close() }()
	for _, q := range []struct {
		pragma string
		want   int
	}{
		{"busy_timeout", 5000},
		{"foreign_keys", 1},
		{"temp_store", 2},
		{"synchronous", 1},
		{"cache_size", -64000},
	} {
		var got int
		if err := conns.QueryRowContext(context.Background(), "PRAGMA "+q.pragma).Scan(&got); err != nil {
			t.Fatalf("reading %s: %v", q.pragma, err)
		}
		if got != q.want {
			t.Errorf("PRAGMA %s = %d, want %d", q.pragma, got, q.want)
		}
	}
}
