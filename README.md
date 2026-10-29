# URL Shortener

A URL shortening service in **Go**, backed by **PostgreSQL** and **Redis**, run with a single `docker compose up`. It focuses on the parts that are easy to get wrong: concurrent writes, caching, and rate limiting.

![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)
![Fiber](https://img.shields.io/badge/Fiber-v2-00ACD7)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-DC382D?logo=redis&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)

## Highlights

- **Fast redirects:** cache-aside with Redis. A hit never touches Postgres; a miss reads Postgres and warms the cache.
- **Batch shortening:** send up to 50 URLs in one request. A bounded worker pool (3 goroutines) processes them and results come back in request order. One bad URL does not fail the batch.
- **Correct under concurrency:** short-code uniqueness is decided by the database primary key (`INSERT ... ON CONFLICT DO NOTHING`), not by check-then-insert. 100 goroutines racing for one code produce exactly one winner.
- **Rate limiting:** per-IP quota using atomic Redis `SETNX` + `DECR`. Returns `429` with `Retry-After` when exhausted.
- **Custom codes and expiry:** choose your own short code and a lifetime in hours.
- **Read/write split:** writes go to the primary; reads can be spread across replicas (round-robin). Falls back to the primary when no replicas are configured.
- **Operational basics:** multi-stage Docker build running as a non-root user, healthchecks so the API waits for Postgres and Redis, graceful shutdown on SIGTERM, structured logs.
- **Tested:** unit, handler and real-Postgres integration tests, including race scenarios, run with Go's race detector.

## Architecture

```mermaid
flowchart LR
    client([Client])
    subgraph api[Fiber API - Go]
        shorten["POST /api/v1<br/>worker pool of 3"]
        redirect["GET /:code<br/>302 redirect"]
        manage["PUT / DELETE<br/>/api/v1/:code"]
    end
    cache[("Redis db 0<br/>URL cache")]
    limits[("Redis db 1<br/>rate limits")]
    pg[("PostgreSQL<br/>source of truth")]

    client --> shorten
    client --> redirect
    client --> manage
    shorten --> limits
    shorten --> pg
    shorten --> cache
    redirect --> cache
    redirect -.cache miss.-> pg
    manage --> pg
    manage --> cache
```

**Redirect path:** Redis `GET` → on a miss, Postgres `SELECT` (ignoring expired rows) → warm the cache → `302`.
**Write path:** rate limit → validate → insert into Postgres → write the cache. Postgres is always written first, so the cache is never ahead of the truth.

## Quick start

Requires Docker with Compose.

```bash
git clone https://github.com/<your-username>/url-shortener.git
cd url-shortener
docker compose up --build
```

Then check it is running:

```bash
curl localhost:3001/
# URL Shortening is running
```

Compose starts Redis and Postgres, waits until both are healthy, applies the schema from `api/database/init/`, then starts the API on port **3001**.

To stop everything: `docker compose down` (add `-v` to also delete the database volume).

> **Windows PowerShell:** use `curl.exe` (plain `curl` is an alias for something else) and send JSON from a file, for example `curl.exe -X POST localhost:3001/api/v1 -H "Content-Type: application/json" --data "@body.json"`.

## API

### Create short links: `POST /api/v1`

```bash
curl -X POST localhost:3001/api/v1 \
  -H "Content-Type: application/json" \
  -d '{"url": "https://www.youtube.com/watch?v=3ExDEeSnyvE&t=6496s"}'
```

```json
{
  "responses": [
    {
      "url": "https://www.youtube.com/watch?v=3ExDEeSnyvE&t=6496s",
      "short_url": "localhost:3001/w4ftWP",
      "expiry": 24
    }
  ],
  "x-time-limit-reset": 1800,
  "x-rate-limit-remaining": 9
}
```

Send several at once, with a custom code and expiry. Results keep the order of the request, and failures are reported per URL:

```bash
curl -X POST localhost:3001/api/v1 \
  -H "Content-Type: application/json" \
  -d '{"requests": [
        {"url": "https://example.com/a", "short_url": "promo", "expiry": 48},
        {"url": "not a url"},
        {"url": "https://example.com/c"}
      ]}'
```

```json
{
  "responses": [
    { "url": "https://example.com/a", "short_url": "localhost:3001/promo", "expiry": 48 },
    { "url": "not a url", "expiry": 0, "error": "Bad URL request" },
    { "url": "https://example.com/c", "short_url": "localhost:3001/zeOsyJ", "expiry": 24 }
  ],
  "x-time-limit-reset": 1800,
  "x-rate-limit-remaining": 7
}
```

| Field | Required | Description |
| --- | --- | --- |
| `url` | yes | The long URL. `http://` is added if the scheme is missing. |
| `short_url` | no | Custom code, 3 to 32 characters: letters, digits, `-`, `_`. Random 6-character code if omitted. |
| `expiry` | no | Lifetime in hours. Default 24. |
| `requests` | no | Array of the objects above (max 50) for a batch. |

`x-rate-limit-remaining` is the quota left; `x-time-limit-reset` is the seconds until the quota resets. If every URL in a request is rate limited, the response is `429` with a `Retry-After` header.

### Redirect: `GET /:code`

```bash
curl -i localhost:3001/w4ftWP
# HTTP/1.1 302 Found
# Location: https://www.youtube.com/watch?v=3ExDEeSnyvE&t=6496s
```

A `302` (not `301`) is used on purpose, so browsers do not cache the redirect forever and updates and deletes take effect. Unknown or expired codes return `404`.

### Update: `PUT /api/v1/:code`

```bash
curl -X PUT localhost:3001/api/v1/promo \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/new-target"}'
```

### Delete: `DELETE /api/v1/:code`

```bash
curl -X DELETE localhost:3001/api/v1/promo
```

## Configuration

All configuration is through environment variables. Copy `.env.example` to `.env` when running outside Docker (Compose sets these itself).

| Variable | Default | Description |
| --- | --- | --- |
| `APP_PORT` | `:3001` | Address the API listens on. |
| `DOMAIN` | none | Prefix for returned short URLs, for example `localhost:3001`. |
| `API_QUOTA` | `10` | URLs each IP may shorten per 30-minute window. |
| `DB_ADDR` | `redis:6379` | Redis address. |
| `DB_PASS` | empty | Redis password. |
| `PG_HOST`, `PG_PORT` | none | Postgres host and port. |
| `PG_USER`, `PG_PASS`, `PG_DB` | none | Postgres credentials and database name. |
| `PG_SSLMODE` | `disable` | Postgres SSL mode. Use `require` or stricter in production. |
| `REPLICA_DB_HOSTS` | empty | Comma-separated replica hosts for reads. Empty means reads use the primary. |

## Project structure

```
.
├── docker-compose.yml        # API + Postgres + Redis, with healthchecks
├── .env.example
├── request.http              # example requests
└── api/
    ├── Dockerfile            # multi-stage build, non-root user
    ├── main.go               # wiring, routes, graceful shutdown
    ├── database/
    │   ├── database.go       # Redis client factory
    │   ├── interfaces.go     # Storage and Cache interfaces
    │   ├── postgres_storage.go   # primary + replicas, queries
    │   ├── redis_cache.go
    │   ├── init/001_schema.sql
    │   └── *_test.go
    ├── helpers/              # URL normalisation helpers
    └── routes/
        ├── routes.go         # Handler (dependencies injected)
        ├── shortenURL.go     # batch create, worker pool, rate limit
        ├── resolveURL.go     # redirect
        ├── updateURL.go      # update and delete
        └── *_test.go
```

## Design decisions

- **The database decides uniqueness.** A check-then-insert (even against the cache) lets two concurrent requests both pass the check. `INSERT ... ON CONFLICT DO NOTHING` makes the check and the write one atomic step, and it still works with many API instances.
- **The cache is an optimisation, never a dependency.** If Redis is down, reads fall back to Postgres and writes still succeed.
- **Redirects return 302.** It keeps updates, deletes and click counting working.
- **Bounded concurrency.** A worker pool caps how many database operations one batch can have in flight. Results are collected by index so order is preserved.
- **Handlers depend on small interfaces** (`Storage`, `Cache`), so tests use in-memory fakes with no Docker.
- **Rate limiting uses atomic Redis commands,** so the quota cannot be overspent by concurrent workers or multiple instances.

## Testing

```bash
# unit and handler tests, with the race detector and coverage
go test -race -cover ./...

# Postgres integration tests (needs a running Postgres)
docker compose up -d postgres
export TEST_PG_DSN="postgres://postgres:devpass@localhost:5432/shortener?sslmode=disable"
go test -race -tags=integration -v ./api/database/...
```

PowerShell: `$env:TEST_PG_DSN="postgres://postgres:devpass@localhost:5432/shortener?sslmode=disable"`. If `-race` fails on Windows because of a missing C compiler, run without it.

### What the tests verify

| Behaviour | How it is verified |
| --- | --- |
| Create, redirect, update, delete, 404s | Handler tests through real HTTP routing |
| Batch results keep request order; one bad URL does not fail the batch | 30-URL batch test; mixed valid and invalid batch test |
| Only one request can claim a custom code | 50 concurrent handler requests, exactly 1 success and 49 conflicts |
| Same, against the real database | 100 concurrent inserts into Postgres, exactly 1 success and 99 `ErrShortURLExists` |
| Rate limit enforced, quota not overspent | Quota 3: requests 4 and 5 return 429 with `Retry-After`. A batch of 4 against quota 2 gives 2 successes and 2 rejections |
| Cache hit, miss with warm-up, expiry, and Redis outage | Cache and handler tests (TTL tested with a fake clock) |
| Expired rows are not returned | Postgres integration test |

### Results

<!-- Paste the real output of your own runs below. -->

```text
$ go test -race -cover ./...
<paste output here>
```

```text
$ go test -race -tags=integration -v ./api/database/...
<paste output here>
```

Rate-limit demo (quota 10; requests 11 and 12 should be rejected):

```text
<paste output here>
```

## Known limitations and roadmap

- No authentication yet: anyone who knows a short code can update or delete it. Ownership checks are the first planned feature.
- Fixed-window rate limiting; limits are per IP (behind a proxy, configure the real client IP).
- The Compose file runs a single Postgres. Read-replica routing exists in code, but replication is not set up here.
- Expired rows are filtered out of reads but not yet deleted by a cleanup job.
- Planned: CI (GitHub Actions), a k6 load test with published numbers, health and readiness endpoints, Prometheus metrics, schema migrations, per-link click analytics through a queue.
