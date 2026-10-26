package routes_test

import (
	"net/http"
	"testing"
)

func TestResolve_CacheHit(t *testing.T) {
	e := newEnv(t, "100")
	_ = e.cache.Set(bg, "abc123", "https://example.com/target", 0)

	resp, _ := e.do(t, http.MethodGet, "/abc123", "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://example.com/target" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestResolve_CacheMissFallsBackToStoreAndWarmsCache(t *testing.T) {
	e := newEnv(t, "100")
	_ = e.store.Save(bg, "abc123", "https://example.com/from-db", 0)

	if _, ok := e.cache.Get(bg, "abc123"); ok {
		t.Fatal("precondition: cache should be cold")
	}
	resp, _ := e.do(t, http.MethodGet, "/abc123", "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://example.com/from-db" {
		t.Fatalf("status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, ok := e.cache.Get(bg, "abc123"); !ok || got != "https://example.com/from-db" {
		t.Fatalf("cache should now be warm, got %q (found=%v)", got, ok)
	}
}

func TestResolve_Unknown(t *testing.T) {
	e := newEnv(t, "100")
	resp, _ := e.do(t, http.MethodGet, "/nope", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestShortenThenResolve_EndToEnd(t *testing.T) {
	e := newEnv(t, "100")
	_, out := e.shorten(t, `{"url": "https://example.com/e2e"}`)
	code := codeOf(t, out.Responses[0].ShortURL)

	resp, _ := e.do(t, http.MethodGet, "/"+code, "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://example.com/e2e" {
		t.Fatalf("status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t, "100")
	_ = e.store.Save(bg, "abc123", "https://example.com/old", 0)
	_ = e.cache.Set(bg, "abc123", "https://example.com/old", 0)

	resp, _ := e.do(t, http.MethodPut, "/api/v1/abc123", `{"url": "https://example.com/new"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got, _ := e.store.has("abc123"); got != "https://example.com/new" {
		t.Fatalf("store not updated: %q", got)
	}
	if got, _ := e.cache.Get(bg, "abc123"); got != "https://example.com/new" {
		t.Fatalf("cache not updated: %q", got)
	}
}

func TestUpdate_Errors(t *testing.T) {
	e := newEnv(t, "100")

	resp, _ := e.do(t, http.MethodPut, "/api/v1/missing", `{"url": "https://example.com/new"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown code: want 404, got %d", resp.StatusCode)
	}

	_ = e.store.Save(bg, "abc123", "https://example.com/old", 0)
	resp, _ = e.do(t, http.MethodPut, "/api/v1/abc123", `{"url": "not a url"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad url: want 400, got %d", resp.StatusCode)
	}
	if got, _ := e.store.has("abc123"); got != "https://example.com/old" {
		t.Errorf("a rejected update must not change the row, got %q", got)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t, "100")
	_ = e.store.Save(bg, "abc123", "https://example.com/x", 0)
	_ = e.cache.Set(bg, "abc123", "https://example.com/x", 0)

	resp, _ := e.do(t, http.MethodDelete, "/api/v1/abc123", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if _, ok := e.store.has("abc123"); ok {
		t.Fatal("row still in store")
	}
	if _, ok := e.cache.Get(bg, "abc123"); ok {
		t.Fatal("stale entry left in cache")
	}

	// the link must now be dead, and deleting again is a 404
	resp, _ = e.do(t, http.MethodGet, "/abc123", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted link should 404, got %d", resp.StatusCode)
	}
	resp, _ = e.do(t, http.MethodDelete, "/api/v1/abc123", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete: want 404, got %d", resp.StatusCode)
	}
}
