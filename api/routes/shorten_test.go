package routes_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/amireallycodes/myproject/api/routes"
)

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func TestGenerateShortCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		code := routes.GenerateShortCode(6)
		if len(code) != 6 {
			t.Fatalf("want length 6, got %q", code)
		}
		for _, ch := range code {
			if !strings.ContainsRune(alphabet, ch) {
				t.Fatalf("code %q contains %q outside the alphabet", code, ch)
			}
		}
		seen[code] = true
	}
	// 62^6 is ~56 billion, so 1000 codes colliding means the generator is broken
	if len(seen) < 995 {
		t.Fatalf("only %d unique codes out of 1000: generator is not random enough", len(seen))
	}
}

func TestShorten_LegacySingleURLBody(t *testing.T) {
	e := newEnv(t, "100")

	status, out := e.shorten(t, `{"url": "https://www.youtube.com/watch?v=3ExDEeSnyvE&t=6496s"}`)
	if status != http.StatusOK || len(out.Responses) != 1 {
		t.Fatalf("status=%d responses=%d", status, len(out.Responses))
	}
	r := out.Responses[0]
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if !strings.HasPrefix(r.ShortURL, "short.test/") {
		t.Fatalf("short_url %q should start with the DOMAIN", r.ShortURL)
	}
	if r.Expiry != 24 {
		t.Fatalf("default expiry should be 24h, got %d", r.Expiry)
	}

	code := codeOf(t, r.ShortURL)
	if got, ok := e.store.has(code); !ok || got != r.URL {
		t.Fatalf("store has %q (found=%v), want %q", got, ok, r.URL)
	}
	if got, ok := e.cache.Get(bg, code); !ok || got != r.URL {
		t.Fatalf("cache has %q (found=%v), want %q", got, ok, r.URL)
	}
}

func TestShorten_EnforcesHTTP(t *testing.T) {
	e := newEnv(t, "100")
	_, out := e.shorten(t, `{"url": "example.com/some/path"}`)
	if got := out.Responses[0].URL; got != "http://example.com/some/path" {
		t.Fatalf("want http:// prefix, got %q", got)
	}
}

func TestShorten_BatchKeepsOrderAndSavesAll(t *testing.T) {
	e := newEnv(t, "1000")

	var reqs []string
	for i := 0; i < 30; i++ {
		reqs = append(reqs, fmt.Sprintf(`{"url": "https://example.com/page/%d"}`, i))
	}
	status, out := e.shorten(t, `{"requests": [`+strings.Join(reqs, ",")+`]}`)
	if status != http.StatusOK || len(out.Responses) != 30 {
		t.Fatalf("status=%d responses=%d", status, len(out.Responses))
	}

	codes := map[string]bool{}
	for i, r := range out.Responses {
		want := fmt.Sprintf("https://example.com/page/%d", i)
		if r.URL != want {
			t.Fatalf("response %d out of order: got %q want %q", i, r.URL, want)
		}
		if r.Error != "" {
			t.Fatalf("response %d failed: %s", i, r.Error)
		}
		codes[r.ShortURL] = true
	}
	if len(codes) != 30 || e.store.size() != 30 {
		t.Fatalf("want 30 unique codes and 30 stored rows, got %d codes / %d rows", len(codes), e.store.size())
	}
}

func TestShorten_OneBadURLDoesNotFailTheBatch(t *testing.T) {
	e := newEnv(t, "100")
	status, out := e.shorten(t, `{"requests": [
		{"url": "https://good.example.com"},
		{"url": "not a url"},
		{"url": "http://short.test/loop"}
	]}`)
	if status != http.StatusOK || len(out.Responses) != 3 {
		t.Fatalf("status=%d responses=%d", status, len(out.Responses))
	}
	if out.Responses[0].Error != "" {
		t.Fatalf("good url failed: %s", out.Responses[0].Error)
	}
	if out.Responses[1].Error != "Bad URL request" {
		t.Fatalf("bad url: got error %q", out.Responses[1].Error)
	}
	if out.Responses[2].Error != "Cannot hack this system" {
		t.Fatalf("own-domain url: got error %q", out.Responses[2].Error)
	}
	if e.store.size() != 1 {
		t.Fatalf("only the good url should be stored, store has %d", e.store.size())
	}
}

func TestShorten_EmptyAndMalformedBodies(t *testing.T) {
	e := newEnv(t, "100")
	for _, body := range []string{`{}`, `{"requests": []}`, `not json`} {
		resp, _ := e.do(t, http.MethodPost, "/api/v1", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: want 400, got %d", body, resp.StatusCode)
		}
	}
}

func TestShorten_BatchTooLarge(t *testing.T) {
	e := newEnv(t, "1000")
	var reqs []string
	for i := 0; i < 51; i++ {
		reqs = append(reqs, `{"url": "https://example.com"}`)
	}
	resp, _ := e.do(t, http.MethodPost, "/api/v1", `{"requests": [`+strings.Join(reqs, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for 51 urls, got %d", resp.StatusCode)
	}
}

func TestShorten_CustomCode(t *testing.T) {
	e := newEnv(t, "100")

	_, out := e.shorten(t, `{"url": "https://example.com/a", "short_url": "promo"}`)
	if out.Responses[0].ShortURL != "short.test/promo" || out.Responses[0].Error != "" {
		t.Fatalf("first use of custom code failed: %+v", out.Responses[0])
	}

	_, out = e.shorten(t, `{"url": "https://example.com/b", "short_url": "promo"}`)
	if out.Responses[0].Error != "ShortURL already in use" {
		t.Fatalf("second use should be rejected, got %+v", out.Responses[0])
	}
	if got, _ := e.store.has("promo"); got != "https://example.com/a" {
		t.Fatalf("original mapping was overwritten: %q", got)
	}

	_, out = e.shorten(t, `{"url": "https://example.com/c", "short_url": "a/b"}`)
	if out.Responses[0].Error == "" {
		t.Fatalf("custom code containing '/' must be rejected")
	}
}

// Many clients race for the same custom code: exactly one may win.
func TestShorten_ConcurrentSameCustomCode(t *testing.T) {
	e := newEnv(t, "1000")

	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"url": "https://example.com/%d", "short_url": "race"}`, i)
			_, out := e.shorten(t, body)
			mu.Lock()
			defer mu.Unlock()
			switch out.Responses[0].Error {
			case "":
				wins++
			case "ShortURL already in use":
				conflicts++
			default:
				t.Errorf("unexpected error: %s", out.Responses[0].Error)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 || conflicts != n-1 {
		t.Fatalf("want 1 winner and %d conflicts, got %d winners and %d conflicts", n-1, wins, conflicts)
	}
	if e.store.size() != 1 {
		t.Fatalf("store should hold exactly one row, has %d", e.store.size())
	}
}

func TestShorten_RateLimit(t *testing.T) {
	e := newEnv(t, "3")

	for i := 1; i <= 3; i++ {
		status, out := e.shorten(t, `{"url": "https://example.com/x"}`)
		if status != http.StatusOK || out.Responses[0].Error != "" {
			t.Fatalf("request %d should succeed: status=%d resp=%+v", i, status, out.Responses[0])
		}
		if want := 3 - i; out.XRateLimitRemaining != want {
			t.Fatalf("request %d: remaining=%d want %d", i, out.XRateLimitRemaining, want)
		}
	}

	for i := 4; i <= 5; i++ {
		resp, _ := e.do(t, http.MethodPost, "/api/v1", `{"url": "https://example.com/x"}`)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("request %d should be 429, got %d", i, resp.StatusCode)
		}
		if resp.Header.Get("Retry-After") == "" {
			t.Fatalf("request %d: missing Retry-After header", i)
		}
	}
	if e.store.size() != 3 {
		t.Fatalf("only the 3 allowed requests should be stored, got %d", e.store.size())
	}
}

// A batch that is bigger than the remaining quota is cut off partway.
func TestShorten_BatchPartiallyRateLimited(t *testing.T) {
	e := newEnv(t, "2")
	status, out := e.shorten(t, `{"requests": [
		{"url": "https://example.com/1"},
		{"url": "https://example.com/2"},
		{"url": "https://example.com/3"},
		{"url": "https://example.com/4"}
	]}`)
	if status != http.StatusOK {
		t.Fatalf("partial success should be 200, got %d", status)
	}
	ok, limited := 0, 0
	for _, r := range out.Responses {
		switch r.Error {
		case "":
			ok++
		case "rate limit exceeded":
			limited++
		}
	}
	if ok != 2 || limited != 2 {
		t.Fatalf("want 2 ok and 2 rate-limited, got %d and %d", ok, limited)
	}
}
