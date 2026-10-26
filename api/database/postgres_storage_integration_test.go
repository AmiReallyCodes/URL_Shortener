//go:build integration

package database_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/amireallycodes/myproject/api/database"
)

// Run with a real Postgres:
//
//	TEST_PG_DSN="postgres://postgres:devpass@localhost:5432/shortener?sslmode=disable" \
//	  go test -race -tags=integration ./api/database/...
func newCluster(t *testing.T) *database.DBCluster {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; skipping Postgres integration tests")
	}
	ctx := context.Background()
	c, err := database.InitDBCluster(ctx, dsn, nil)
	if err != nil {
		t.Fatalf("cannot connect to Postgres: %v", err)
	}
	t.Cleanup(c.Close)

	// same schema as api/database/init/001_schema.sql
	_, err = c.WriteDB().Exec(ctx, `CREATE TABLE IF NOT EXISTS url_mappings (
		short_url TEXT PRIMARY KEY,
		long_url TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// unique code per test run so tests never collide with real data
func code(t *testing.T, suffix string) string {
	c := fmt.Sprintf("t%d%s", time.Now().UnixNano()%1e9, suffix)
	t.Cleanup(func() {
		if cl := newClusterNoSkip(); cl != nil {
			_, _ = cl.WriteDB().Exec(context.Background(), `DELETE FROM url_mappings WHERE short_url = $1`, c)
			cl.Close()
		}
	})
	return c
}

func newClusterNoSkip() *database.DBCluster {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		return nil
	}
	c, err := database.InitDBCluster(context.Background(), dsn, nil)
	if err != nil {
		return nil
	}
	return c
}

func TestPG_SaveGetUpdateDelete(t *testing.T) {
	c := newCluster(t)
	ctx := context.Background()
	k := code(t, "crud")

	if err := c.Save(ctx, k, "https://example.com/a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(ctx, k); err != nil || got != "https://example.com/a" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := c.Update(ctx, k, "https://example.com/b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(ctx, k); got != "https://example.com/b" {
		t.Fatalf("after Update Get = %q", got)
	}
	if err := c.Delete(ctx, k); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, k); err == nil {
		t.Fatal("Get after Delete should fail")
	}
}

func TestPG_DuplicateSaveReturnsErrShortURLExists(t *testing.T) {
	c := newCluster(t)
	ctx := context.Background()
	k := code(t, "dup")

	if err := c.Save(ctx, k, "https://example.com/first", time.Hour); err != nil {
		t.Fatal(err)
	}
	err := c.Save(ctx, k, "https://example.com/second", time.Hour)
	if !errors.Is(err, database.ErrShortURLExists) {
		t.Fatalf("want ErrShortURLExists, got %v", err)
	}
	if got, _ := c.Get(ctx, k); got != "https://example.com/first" {
		t.Fatalf("the first writer must win, row holds %q", got)
	}
}

func TestPG_UpdateDeleteMissingReturnsNotFound(t *testing.T) {
	c := newCluster(t)
	ctx := context.Background()
	k := code(t, "missing")

	if err := c.Update(ctx, k, "https://example.com"); !errors.Is(err, database.ErrShortURLNotFound) {
		t.Errorf("Update: want ErrShortURLNotFound, got %v", err)
	}
	if err := c.Delete(ctx, k); !errors.Is(err, database.ErrShortURLNotFound) {
		t.Errorf("Delete: want ErrShortURLNotFound, got %v", err)
	}
}

func TestPG_ExpiredRowIsNotReturned(t *testing.T) {
	c := newCluster(t)
	ctx := context.Background()
	k := code(t, "exp")

	if err := c.Save(ctx, k, "https://example.com", -time.Minute); err != nil { // already expired
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, k); err == nil {
		t.Fatal("expired row must not be returned")
	}
}

// 100 goroutines insert the same short code; the primary key must let
// exactly one through.
func TestPG_ConcurrentSaveSameCode(t *testing.T) {
	c := newCluster(t)
	ctx := context.Background()
	k := code(t, "race")

	const n = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, exists, other := 0, 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := c.Save(ctx, k, fmt.Sprintf("https://example.com/%d", i), time.Hour)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, database.ErrShortURLExists):
				exists++
			default:
				other++
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 || exists != n-1 || other != 0 {
		t.Fatalf("want 1 win / %d conflicts, got %d wins / %d conflicts / %d other", n-1, wins, exists, other)
	}
}
