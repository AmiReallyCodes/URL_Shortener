CREATE TABLE IF NOT EXISTS url_mappings (
    short_url TEXT PRIMARY KEY,
    long_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);