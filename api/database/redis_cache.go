package database

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
)

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

func (r *RedisCache) Get(ctx context.Context, short_url string) (string, bool) {
	val, err := r.client.Get(ctx, short_url).Result()
	if err != nil {
		return "", false
	}
	return val, true
}

func (r *RedisCache) Set(ctx context.Context, short_url, longURL string, ttl time.Duration) error {
	return r.client.Set(ctx, short_url, longURL, ttl).Err()
}

func (r *RedisCache) Delete(ctx context.Context, short_url string) error {
	return r.client.Del(ctx, short_url).Err()
}
