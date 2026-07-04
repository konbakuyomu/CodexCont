package executor

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteOpenStoreUsesSingleConnectionAndBusyTimeout(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "executor.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("store should use one DB connection, got %d", got)
	}
	var timeout int
	if err := store.db.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMS {
		t.Fatalf("busy_timeout=%d want %d", timeout, sqliteBusyTimeoutMS)
	}
}
