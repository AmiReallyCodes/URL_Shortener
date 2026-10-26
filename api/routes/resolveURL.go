package routes

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

func (h *Handler) ResolveURL(c *fiber.Ctx) error {

	url := c.Params("url")
	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()
	//check in cache first then postgres DB
	r := h.cache
	value, found := r.Get(ctx, url)
	if !found {
		//return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "Short URL not found in the cache (Redis)"})
		//try in the DB
		pgDB := h.store
		dbvalue, err := pgDB.Get(ctx, url)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "Short URL not found in the cache (Redis) and in DB"})
		}
		value = dbvalue
		h.cache.Set(ctx, url, value, 24*time.Hour)
	}

	rInr := h.rateLimit

	_ = rInr.Incr(ctx, "counter")

	return c.Redirect(value, fiber.StatusFound)
}
