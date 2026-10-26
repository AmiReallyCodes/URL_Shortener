package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/amireallycodes/myproject/api/database"
	"github.com/amireallycodes/myproject/api/routes"
	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func setUpRoutes(app *fiber.App, h *routes.Handler) {
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("URL Shortening is running")
	})
	app.Post("/api/v1", h.ShortenURL)
	app.Get("/api/v1", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"message": "Use POST to create a short URL"})
	})
	app.Put("/api/v1/:url", h.UpdateURL)
	app.Delete("/api/v1/:url", h.DeleteURL)
	app.Get("/:url", h.ResolveURL)
}

// requestLogger logs every request through zerolog so all output has one format.
func requestLogger(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()

	status := c.Response().StatusCode()
	if err != nil {
		var fe *fiber.Error
		if errors.As(err, &fe) {
			status = fe.Code
		} else {
			status = fiber.StatusInternalServerError
		}
	}

	log.Info().
		Str("method", c.Method()).
		Str("path", c.Path()).
		Int("status", status).
		Dur("latency", time.Since(start)).
		Msg("request")
	return err
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildDSN builds a Postgres connection string for a host. Using url.URL
// escapes special characters in the password, which fmt.Sprintf would not.
func buildDSN(host string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(os.Getenv("PG_USER"), os.Getenv("PG_PASS")),
		Host:     net.JoinHostPort(host, os.Getenv("PG_PORT")),
		Path:     "/" + os.Getenv("PG_DB"),
		RawQuery: "sslmode=" + getenv("PG_SSLMODE", "disable"),
	}
	return u.String()
}

func run() error {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

	if err := godotenv.Load(); err != nil {
		log.Warn().Msg("no .env file found, relying on environment")
	}

	rawRedis := database.CreateClient(0)      // URL storage
	cache := database.NewRedisCache(rawRedis) // cache wrapper
	rateLimit := database.CreateClient(1)     // rate-limit counters
	defer rawRedis.Close()
	defer rateLimit.Close()

	primaryDSN := buildDSN(os.Getenv("PG_HOST"))

	var replicaDSNs []string
	for _, host := range strings.Split(os.Getenv("REPLICA_DB_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			replicaDSNs = append(replicaDSNs, buildDSN(host))
		}
	}

	dbCluster, err := database.InitDBCluster(context.Background(), primaryDSN, replicaDSNs)
	if err != nil {
		return err
	}
	defer dbCluster.Close()

	h := routes.NewHandler(dbCluster, cache, rateLimit)

	app := fiber.New()
	app.Use(requestLogger)
	setUpRoutes(app, h)

	// Shut down cleanly on Ctrl+C or `docker stop`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	port := getenv("APP_PORT", ":3001")
	listenErr := make(chan error, 1)
	go func() { listenErr <- app.Listen(port) }()

	select {
	case err := <-listenErr:
		return err
	case <-ctx.Done():
		log.Info().Msg("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return app.ShutdownWithContext(shutdownCtx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("application error")
	}
}
