package database

import (
	"os"
	"strings"

	"github.com/go-redis/redis/v8"
)

func resolveRedisAddr() string {
	addr := strings.TrimSpace(os.Getenv("DB_ADDR"))
	addr = strings.Trim(addr, `"`)
	if addr == "" {
		addr = "redis:6379"
	}
	return addr
}

func CreateClient(dbNo int) *redis.Client {
	db := redis.NewClient(&redis.Options{
		Addr:     resolveRedisAddr(),
		Password: os.Getenv("DB_PASS"),
		DB:       dbNo,
	})
	return db
}
