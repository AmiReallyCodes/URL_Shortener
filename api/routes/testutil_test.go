package routes_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/amireallycodes/myproject/api/database"
	"github.com/amireallycodes/myproject/api/routes"
	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
)

// ---- in-memory fakes -------------------------------------------------------

// fakeStore mimics Postgres: Save is atomic and rejects duplicates, exactly
// like INSERT ... ON CONFLICT DO NOTHING does.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]string{}} }

func (s *fakeStore) Save(_ context.Context, code, long string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[code]; ok {
		return database.ErrShortURLExists
	}
	s.m[code] = long
	return nil
}

func (s *fakeStore) Get(_ context.Context, code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[code]
	if !ok {
		return "", database.ErrShortURLNotFound
	}
	return v, nil
}

func (s *fakeStore) Update(_ context.Context, code, long string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[code]; !ok {
		return database.ErrShortURLNotFound
	}
	s.m[code] = long
	return nil
}

func (s *fakeStore) Delete(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[code]; !ok {
		return database.ErrShortURLNotFound
	}
	delete(s.m, code)
	return nil
}

func (s *fakeStore) has(code string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[code]
	return v, ok
}

func (s *fakeStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

type fakeCache struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeCache() *fakeCache { return &fakeCache{m: map[string]string{}} }

func (c *fakeCache) Get(_ context.Context, code string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[code]
	return v, ok
}

func (c *fakeCache) Set(_ context.Context, code, long string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[code] = long
	return nil
}

func (c *fakeCache) Delete(_ context.Context, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, code)
	return nil
}

// ---- test app --------------------------------------------------------------

type env struct {
	app   *fiber.App
	store *fakeStore
	cache *fakeCache
}

// newEnv wires the real handlers to fake storage/cache and an in-memory Redis
// (for the rate limiter). quota is the API_QUOTA per client.
func newEnv(t *testing.T, quota string) *env {
	t.Helper()
	t.Setenv("API_QUOTA", quota)
	t.Setenv("DOMAIN", "short.test")

	mr := miniredis.RunT(t)
	rl := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rl.Close() })

	e := &env{app: fiber.New(), store: newFakeStore(), cache: newFakeCache()}
	h := routes.NewHandler(e.store, e.cache, rl)

	// keep in sync with setUpRoutes in api/main.go
	e.app.Post("/api/v1", h.ShortenURL)
	e.app.Put("/api/v1/:url", h.UpdateURL)
	e.app.Delete("/api/v1/:url", h.DeleteURL)
	e.app.Get("/:url", h.ResolveURL)
	return e
}

func (e *env) do(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.app.Test(req, -1) // -1: no timeout
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

type item struct {
	URL      string `json:"url"`
	ShortURL string `json:"short_url"`
	Expiry   int    `json:"expiry"`
	Error    string `json:"error"`
}

type shortenResp struct {
	Responses           []item `json:"responses"`
	XTimeLimitReset     int    `json:"x-time-limit-reset"`
	XRateLimitRemaining int    `json:"x-rate-limit-remaining"`
}

func (e *env) shorten(t *testing.T, body string) (int, shortenResp) {
	t.Helper()
	resp, b := e.do(t, http.MethodPost, "/api/v1", body)
	var out shortenResp
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("bad json %q: %v", b, err)
	}
	return resp.StatusCode, out
}

// codeOf extracts "abc123" from "short.test/abc123".
func codeOf(t *testing.T, shortURL string) string {
	t.Helper()
	parts := strings.SplitN(shortURL, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		t.Fatalf("unexpected short_url %q", shortURL)
	}
	return parts[1]
}

// bg is the context passed to the fakes.
var bg = context.Background()
