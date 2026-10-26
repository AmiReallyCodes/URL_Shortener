package database

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// ErrShortURLExists is returned by Save when the short_url already exists
// in the database (i.e. the INSERT hit the ON CONFLICT DO NOTHING branch).
var ErrShortURLExists = errors.New("short url already exists")

// ErrShortURLNotFound is returned by Update/Delete when the short_url doesn't exist
var ErrShortURLNotFound = errors.New("short url not found")

type DBCluster struct {
	dbPrimary  *pgxpool.Pool
	dbReplicas []*pgxpool.Pool
	counter    atomic.Uint64
}

func InitDBCluster(ctx context.Context, primaryDns string, secondaryDNS []string) (*DBCluster, error) {
	dbPrimary, err := pgxpool.New(ctx, primaryDns)
	if err != nil {
		return nil, err
	}
	// pgxpool.New connects lazily, so ping to fail fast if Postgres is unreachable
	if err := dbPrimary.Ping(ctx); err != nil {
		dbPrimary.Close()
		return nil, err
	}

	var dbReplicas []*pgxpool.Pool
	for i, dbs := range secondaryDNS {
		dbSec, err := pgxpool.New(ctx, dbs)
		if err != nil {
			// do not log the DSN: it contains the password
			log.Warn().Err(err).Int("replica", i).Msg("failed to connect to replica")
			continue
		}
		dbReplicas = append(dbReplicas, dbSec)
	}
	return &DBCluster{dbPrimary: dbPrimary, dbReplicas: dbReplicas}, nil
}

// Close releases every connection pool.
func (dbc *DBCluster) Close() {
	dbc.dbPrimary.Close()
	for _, r := range dbc.dbReplicas {
		r.Close()
	}
}

// for write use single primary db
func (dbc *DBCluster) Save(ctx context.Context, short_url, longURL string, expiry time.Duration) error {
	tag, err := dbc.WriteDB().Exec(ctx,
		`INSERT INTO url_mappings (short_url, long_url, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (short_url) DO NOTHING`,
		short_url, longURL, time.Now().Add(expiry))
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		// no row was inserted -> short_url was already taken
		return ErrShortURLExists
	}

	return nil
}

func (dbc *DBCluster) ReadDB() *pgxpool.Pool {
	if len(dbc.dbReplicas) == 0 {
		return dbc.dbPrimary
	}
	idx := dbc.counter.Add(1) % uint64(len(dbc.dbReplicas))
	return dbc.dbReplicas[idx]
}

func (dbc *DBCluster) WriteDB() *pgxpool.Pool {
	return dbc.dbPrimary
}

// for reading use replicas i.e. secondary dbs[]
func (dbc *DBCluster) Get(ctx context.Context, short_url string) (string, error) {
	var longURL string
	err := dbc.ReadDB().QueryRow(ctx,
		`SELECT long_url FROM url_mappings WHERE short_url = $1 AND (expires_at IS NULL OR expires_at > now())`,
		short_url).Scan(&longURL)
	return longURL, err
}

func (dbc *DBCluster) Update(ctx context.Context, short_url, newLongURL string) error {
	tag, err := dbc.WriteDB().Exec(ctx,
		`UPDATE url_mappings SET long_url = $1 WHERE short_url = $2`,
		newLongURL, short_url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrShortURLNotFound
	}
	return nil
}

func (dbc *DBCluster) Delete(ctx context.Context, short_url string) error {
	tag, err := dbc.WriteDB().Exec(ctx,
		`DELETE FROM url_mappings WHERE short_url = $1`,
		short_url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrShortURLNotFound
	}
	return nil
}
