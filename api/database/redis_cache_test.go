package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/amireallycodes/myproject/api/database"
	"github.com/go-redis/redis/v8"
)

func newCache(t *testing.T) (*database.RedisCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return database.NewRedisCache(client), mr
}

func TestRedisCache_SetGetDelete(t *testing.T) {
	c, _ := newCache(t)
	ctx := context.Background()

	if _, ok := c.Get(ctx, "abc"); ok {
		t.Fatal("empty cache must miss")
	}
	if err := c.Set(ctx, "abc", "https://example.com", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get(ctx, "abc"); !ok || got != "https://example.com" {
		t.Fatalf("got %q (found=%v)", got, ok)
	}
	if err := c.Delete(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(ctx, "abc"); ok {
		t.Fatal("deleted key must miss")
	}
}

func TestRedisCache_EntryExpires(t *testing.T) {
	c, mr := newCache(t)
	ctx := context.Background()

	_ = c.Set(ctx, "abc", "https://example.com", time.Minute)
	mr.FastForward(2 * time.Minute) // move miniredis' clock instead of sleeping

	if _, ok := c.Get(ctx, "abc"); ok {
		t.Fatal("entry should have expired")
	}
}

func TestRedisCache_RedisDownIsAMiss(t *testing.T) {
	c, mr := newCache(t)
	mr.Close()

	if _, ok := c.Get(context.Background(), "abc"); ok {
		t.Fatal("when Redis is unreachable Get must report a miss, not a hit")
	}
}
