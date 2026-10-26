package routes

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/amireallycodes/myproject/api/database"
	"github.com/amireallycodes/myproject/api/helpers"
	"github.com/asaskevich/govalidator"
	"github.com/gofiber/fiber/v2"
)

type updateRequest struct {
	URL string `json:"url"`
}

func (h *Handler) UpdateURL(c *fiber.Ctx) error {
	shortURL := c.Params("url")

	body := new(updateRequest)
	if err := c.BodyParser(body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "Invalid request body"})
	}

	if !govalidator.IsURL(body.URL) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"message": "Bad URL request"})
	}
	if !helpers.RemoveDoaminError(body.URL) {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Cannot hack this system"})
	}
	body.URL = helpers.EnforceHTTP(body.URL)

	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	if err := h.store.Update(ctx, shortURL, body.URL); err != nil {
		if errors.Is(err, database.ErrShortURLNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "Short URL not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   "Unable to update URL in database.",
			"details": err.Error(),
		})
	}

	// keep cache consistent — overwrite with the new long_url rather than just deleting,
	// so the next redirect doesn't need to fall back to Postgres
	_ = h.cache.Set(ctx, shortURL, body.URL, 24*time.Hour)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":   "Short URL updated",
		"short_url": os.Getenv("DOMAIN") + "/" + shortURL,
		"url":       body.URL,
	})
}

func (h *Handler) DeleteURL(c *fiber.Ctx) error {
	shortURL := c.Params("url")

	ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
	defer cancel()

	if err := h.store.Delete(ctx, shortURL); err != nil {
		if errors.Is(err, database.ErrShortURLNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"message": "Short URL not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":   "Unable to delete URL from database.",
			"details": err.Error(),
		})
	}

	// invalidate cache so a stale redirect doesn't linger
	_ = h.cache.Delete(ctx, shortURL)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Short URL deleted"})
}
