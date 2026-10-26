package routes

import (
	"github.com/amireallycodes/myproject/api/database"
	"github.com/go-redis/redis/v8"
)

// instead of rdb *redis.Client i am using postgres or cache depending on requirement
type Handler struct {
	store     database.Storage //postgres
	cache     database.Cache   //redis
	rateLimit *redis.Client
}

func NewHandler(store database.Storage, cache database.Cache, rateLimit *redis.Client) *Handler {
	return &Handler{store: store, cache: cache, rateLimit: rateLimit}
}
