package database

import (
	"context"
	"time"
)

type Storage interface {
	Save(ctx context.Context, short_url, longURL string, expiry time.Duration) error
	Get(ctx context.Context, short_url string) (string, error)
	Delete(ctx context.Context, short_url string) error
	Update(ctx context.Context, short_url, newLongURL string) error
}

type Cache interface {
	Get(ctx context.Context, short_url string) (string, bool)
	Set(ctx context.Context, short_url, longURL string, ttl time.Duration) error
	Delete(ctx context.Context, short_url string) error
}
