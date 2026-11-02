-- Server-side login sessions. Only an HMAC of the session token is stored,
-- so someone who reads the database cannot reuse a live session cookie.
-- Times are Unix seconds to avoid any time-zone parsing ambiguity.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

-- Speeds up the per-user queries the app runs on every page.
CREATE INDEX IF NOT EXISTS idx_history_user ON history(user_id, viewed_at);
CREATE INDEX IF NOT EXISTS idx_comments_date ON comments(image_date, created_at);
