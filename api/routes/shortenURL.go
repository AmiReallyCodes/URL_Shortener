package routes

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/amireallycodes/myproject/api/database"
	"github.com/amireallycodes/myproject/api/helpers"
	"github.com/asaskevich/govalidator"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
)

const (
	workersCount     = 3
	maxBatchSize     = 50
	maxCodeAttempts  = 5
	shortCodeLength  = 6
	defaultExpiryHrs = 24
	rateLimitWindow  = 30 * time.Minute
	defaultQuota     = 10

	// create random shortURL
	alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	errRateLimited = "rate limit exceeded"
)

// custom short codes must be a single URL path segment
var customCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

type request struct {
	URL      string `json:"url"`
	API      string `json:"api"`
	ShortURL string `json:"short_url"`
	Expiry   int    `json:"expiry"`
}

// response is the result for ONE url in a batch.
type response struct {
	URL      string `json:"url"`
	ShortURL string `json:"short_url,omitempty"`
	Expiry   int    `json:"expiry"`
	Error    string `json:"error,omitempty"`
}

// batchRequest lets one client send many urls at once. The embedded request
// also keeps the old single-url body {"url": "..."} working.
type batchRequest struct {
	Requests []request `json:"requests"`
	request
}

// batchResponse is the single JSON response for the whole batch.
// x-time-limit-reset is in seconds.
type batchResponse struct {
	Responses           []response `json:"responses"`
	XTimeLimitReset     int        `json:"x-time-limit-reset"`
	XRateLimitRemaining int        `json:"x-rate-limit-remaining"`
}

// GlobalCounter counts how many urls of a batch were shortened successfully.
type GlobalCounter struct {
	m       sync.Mutex
	counter int
}

func (g *GlobalCounter) Inc() {
	g.m.Lock()
	g.counter++
	g.m.Unlock()
}

func (g *GlobalCounter) Value() int {
	g.m.Lock()
	defer g.m.Unlock()
	return g.counter
}

func (r *request) Validate() {
	if r.URL == "" {
		r.URL = r.API
	}
}

// GenerateShortCode returns n random characters from alphabet.
// Go 1.20+ seeds math/rand automatically, and the global source is safe
// for concurrent use.
func GenerateShortCode(n int) string {
	code := make([]byte, n)
	for i := range code {
		code[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(code)
}

type job struct {
	idx int
	req request
}

type result struct {
	idx  int
	resp response
}

func (h *Handler) ShortenURL(c *fiber.Ctx) error {
	body := new(batchRequest)
	if err := c.BodyParser(body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "Invalid request body"})
	}

	reqs := body.Requests
	if len(reqs) == 0 && (body.request.URL != "" || body.request.API != "") {
		reqs = []request{body.request} // legacy single-url body
	}
	if len(reqs) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "No URLs provided"})
	}
	if len(reqs) > maxBatchSize {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "Too many URLs in one batch (max " + strconv.Itoa(maxBatchSize) + ")"})
	}
	for i := range reqs {
		reqs[i].Validate()
	}

	// the workers only get what they need, not the whole fiber.Ctx
	clientIP := c.IP()
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()

	jobs := make(chan job, len(reqs))
	results := make(chan result, len(reqs))
	gc := &GlobalCounter{}

	nWorkers := workersCount
	if len(reqs) < nWorkers {
		nWorkers = len(reqs)
	}
	var wg sync.WaitGroup
	for i := 0; i < nWorkers; i++ {
		wg.Add(1)
		go h.worker(ctx, clientIP, jobs, results, gc, &wg)
	}

	for i, r := range reqs {
		jobs <- job{idx: i, req: r}
	}
	// without close(jobs) the workers would block on `range jobs` forever
	close(jobs)

	// close results only after every worker has finished; in its own goroutine
	// so the range below can start draining right away
	go func() {
		wg.Wait()
		close(results)
	}()

	// results arrive in any order; idx puts them back in request order
	out := make([]response, len(reqs))
	rateLimited := 0
	for r := range results {
		out[r.idx] = r.resp
		if r.resp.Error == errRateLimited {
			rateLimited++
		}
	}

	log.Info().Int("requested", len(reqs)).Int("shortened", gc.Value()).Msg("batch finished")

	remaining, reset := h.rateStatus(ctx, clientIP)
	status := fiber.StatusOK
	if rateLimited == len(reqs) {
		status = fiber.StatusTooManyRequests
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(reset))
	}

	return c.Status(status).JSON(batchResponse{
		Responses:           out,
		XTimeLimitReset:     reset,
		XRateLimitRemaining: remaining,
	})
}

func (h *Handler) worker(ctx context.Context, ip string, jobs <-chan job, results chan<- result, gc *GlobalCounter, wg *sync.WaitGroup) {
	defer wg.Done()
	for j := range jobs {
		results <- result{idx: j.idx, resp: h.shortenOne(ctx, ip, j.req, gc)}
	}
}

// shortenOne handles a single url of a batch. Failures go into response.Error
// so one bad url does not fail the whole batch.
func (h *Handler) shortenOne(ctx context.Context, ip string, r request, gc *GlobalCounter) response {
	res := response{URL: r.URL, Expiry: r.Expiry}

	allowed, err := h.allow(ctx, ip)
	if err != nil {
		log.Error().Err(err).Msg("rate limiter failure")
		res.Error = "rate limiter unavailable"
		return res
	}
	if !allowed {
		res.Error = errRateLimited
		return res
	}

	if !govalidator.IsURL(r.URL) {
		res.Error = "Bad URL request"
		return res
	}
	if !helpers.RemoveDoaminError(r.URL) {
		res.Error = "Cannot hack this system"
		return res
	}
	if r.ShortURL != "" && !customCodePattern.MatchString(r.ShortURL) {
		res.Error = "short_url must be 3-32 characters: letters, digits, - or _"
		return res
	}
	if r.Expiry < 0 {
		res.Error = "expiry must be a positive number of hours"
		return res
	}

	r.URL = helpers.EnforceHTTP(r.URL)
	if r.Expiry == 0 {
		r.Expiry = defaultExpiryHrs
	}
	res.URL, res.Expiry = r.URL, r.Expiry
	ttl := time.Duration(r.Expiry) * time.Hour

	// Postgres is the source of truth. Its unique key decides who wins when
	// two requests want the same code, so there is no check-then-set race.
	code, err := h.saveWithCode(ctx, r, ttl)
	if err != nil {
		if errors.Is(err, database.ErrShortURLExists) {
			res.Error = "ShortURL already in use"
		} else {
			log.Error().Err(err).Msg("unable to save url")
			res.Error = "Unable to save URL"
		}
		return res
	}

	// the cache is only an optimisation; a failure here must not fail the request
	if err := h.cache.Set(ctx, code, r.URL, ttl); err != nil {
		log.Warn().Err(err).Str("code", code).Msg("unable to warm cache")
	}

	gc.Inc()
	res.ShortURL = os.Getenv("DOMAIN") + "/" + code
	return res
}

// saveWithCode saves the url under the custom code, or under a random code
// (retrying on the rare collision).
func (h *Handler) saveWithCode(ctx context.Context, r request, ttl time.Duration) (string, error) {
	if r.ShortURL != "" {
		return r.ShortURL, h.store.Save(ctx, r.ShortURL, r.URL, ttl)
	}
	for i := 0; i < maxCodeAttempts; i++ {
		code := GenerateShortCode(shortCodeLength)
		err := h.store.Save(ctx, code, r.URL, ttl)
		if errors.Is(err, database.ErrShortURLExists) {
			continue
		}
		return code, err
	}
	return "", errors.New("could not generate a unique short code")
}

func rateKey(ip string) string { return "rl:" + ip }

func quotaFromEnv() int {
	if q, err := strconv.Atoi(os.Getenv("API_QUOTA")); err == nil && q > 0 {
		return q
	}
	return defaultQuota
}

// allow takes one unit of the client's quota. SETNX seeds the counter with an
// expiry only if it does not exist yet, and DECR is atomic, so concurrent
// workers cannot overspend the quota.
func (h *Handler) allow(ctx context.Context, ip string) (bool, error) {
	key := rateKey(ip)
	if err := h.rateLimit.SetNX(ctx, key, quotaFromEnv(), rateLimitWindow).Err(); err != nil {
		return false, err
	}
	remaining, err := h.rateLimit.Decr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return remaining >= 0, nil
}

// rateStatus returns the remaining quota and seconds until the window resets.
func (h *Handler) rateStatus(ctx context.Context, ip string) (remaining, resetSeconds int) {
	key := rateKey(ip)
	if val, err := h.rateLimit.Get(ctx, key).Result(); err == nil {
		remaining, _ = strconv.Atoi(val)
	}
	if remaining < 0 {
		remaining = 0
	}
	if ttl, err := h.rateLimit.TTL(ctx, key).Result(); err == nil && ttl > 0 {
		resetSeconds = int(ttl.Seconds())
	}
	return remaining, resetSeconds
}
